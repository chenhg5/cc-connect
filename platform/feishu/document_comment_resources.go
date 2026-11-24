package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkdocx "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
	larkdrive "github.com/larksuite/oapi-sdk-go/v3/service/drive/v1"
	larksheets "github.com/larksuite/oapi-sdk-go/v3/service/sheets/v3"
)

// Preserve sheet positioning metadata not represented by the SDK.
func commentReplySheetID(raw []byte, replyID string) string {
	var result struct {
		Data struct {
			Items []struct {
				ReplyID string `json:"reply_id"`
				Extra   struct {
					NotifyExtra struct {
						Sheet string `json:"sheet"`
					} `json:"notify_extra"`
				} `json:"extra"`
			} `json:"items"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return ""
	}
	for _, reply := range result.Data.Items {
		if reply.ReplyID == replyID {
			return reply.Extra.NotifyExtra.Sheet
		}
	}
	return ""
}

func (p *Platform) documentResourceContext(ctx context.Context, rc documentReplyContext, quote, sheetID string) string {
	if rc.fileType == "docx" {
		body, err := p.client.Docx.Document.RawContent(ctx, larkdocx.NewRawContentDocumentReqBuilder().DocumentId(rc.fileToken).Build())
		if err != nil {
			return "Resource content unavailable：" + err.Error()
		}
		if !body.Success() || body.Data == nil {
			return fmt.Sprintf("Resource content unavailable：code=%d %s", body.Code, body.Msg)
		}
		return stringValue(body.Data.Content)
	}
	var sections []string
	meta, err := p.client.Drive.Meta.BatchQuery(ctx, larkdrive.NewBatchQueryMetaReqBuilder().MetaRequest(&larkdrive.MetaRequest{RequestDocs: []*larkdrive.RequestDoc{{DocToken: &rc.fileToken, DocType: &rc.fileType}}}).Build())
	if err != nil {
		sections = append(sections, "Resource metadata unavailable："+err.Error())
	} else if !meta.Success() || meta.Data == nil {
		sections = append(sections, fmt.Sprintf("Resource metadata unavailable：code=%d %s", meta.Code, meta.Msg))
	} else {
		for _, item := range meta.Data.Metas {
			if item == nil {
				continue
			}
			sections = append(sections, "Resource title："+stringValue(item.Title))
			if link := stringValue(item.Url); link != "" {
				sections = append(sections, "Resource URL："+link)
			}
		}
	}
	if rc.fileType == "sheet" {
		body, err := p.documentSheetContext(ctx, rc.fileToken, quote, sheetID)
		if err != nil {
			sections = append(sections, "Spreadsheet content unavailable："+err.Error())
		} else {
			sections = append(sections, body)
		}
	} else {
		sections = append(sections, "Only comments, quotes and resource metadata were fetched for this type. Use available tools if more resource content is needed.")
	}
	return strings.Join(sections, "\n")
}

var commentSheetCell = regexp.MustCompile(`^\$?([A-Z]+)\$?([1-9][0-9]*)(?:\s|$)`)

func (p *Platform) documentSheetContext(ctx context.Context, token, quote, sheetID string) (string, error) {
	if sheetID == "" {
		sheets, err := p.client.Sheets.SpreadsheetSheet.Query(ctx, larksheets.NewQuerySpreadsheetSheetReqBuilder().SpreadsheetToken(token).Build())
		if err != nil {
			return "", err
		}
		if !sheets.Success() || sheets.Data == nil {
			return "", fmt.Errorf("list sheets: code=%d %s", sheets.Code, sheets.Msg)
		}
		if len(sheets.Data.Sheets) == 1 && sheets.Data.Sheets[0] != nil {
			sheetID = stringValue(sheets.Data.Sheets[0].SheetId)
		} else {
			var candidates []string
			for _, sheet := range sheets.Data.Sheets {
				if sheet != nil {
					candidates = append(candidates, stringValue(sheet.Title)+"="+stringValue(sheet.SheetId))
				}
			}
			return "", fmt.Errorf("comment did not identify a sheet. Available sheets: %s. Locate the quoted content; no sheet was selected automatically", strings.Join(candidates, ", "))
		}
	}
	resp, err := p.client.Sheets.SpreadsheetSheet.Get(ctx, larksheets.NewGetSpreadsheetSheetReqBuilder().SpreadsheetToken(token).SheetId(sheetID).Build())
	if err != nil {
		return "", err
	}
	if !resp.Success() || resp.Data == nil || resp.Data.Sheet == nil {
		return "", fmt.Errorf("get sheet: code=%d %s", resp.Code, resp.Msg)
	}
	sheet := resp.Data.Sheet
	columns, rows := 26, 200
	if grid := sheet.GridProperties; grid != nil {
		if grid.ColumnCount != nil {
			columns = *grid.ColumnCount
		}
		if grid.RowCount != nil {
			rows = *grid.RowCount
		}
	}
	first, last := 1, min(20, rows)
	if cell := commentSheetCell.FindStringSubmatch(quote); cell != nil {
		row, err := strconv.Atoi(cell[2])
		if err == nil && row <= rows {
			first = max(1, row-2)
			last = min(rows, row+2)
		}
	}
	// Prefetch the header and rows near the quoted cell; leave broader reads to the agent.
	endColumn := sheetColumnName(min(columns, 100))
	ranges := []string{fmt.Sprintf("%s!A%d:%s%d", sheetID, first, endColumn, last)}
	if first > 1 {
		ranges = append([]string{sheetID + "!A1:" + endColumn + "1"}, ranges...)
	}
	parts := []string{"Sheet: " + stringValue(sheet.Title) + " (" + sheetID + ")", "Location from the comment quote: " + quote}
	for _, cellRange := range ranges {
		path := "/open-apis/sheets/v2/spreadsheets/" + url.PathEscape(token) + "/values/" + url.PathEscape(cellRange) + "?valueRenderOption=ToString&dateTimeRenderOption=FormattedString&user_id_type=open_id"
		r, err := p.client.Get(ctx, path, nil, larkcore.AccessTokenTypeTenant)
		if err != nil {
			return "", err
		}
		var result struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				ValueRange json.RawMessage `json:"valueRange"`
			} `json:"data"`
		}
		if err = json.Unmarshal(r.RawBody, &result); err != nil {
			return "", err
		}
		if result.Code != 0 {
			return "", fmt.Errorf("read cells: code=%d %s", result.Code, result.Msg)
		}
		parts = append(parts, "Cell range "+cellRange+":\n"+string(result.Data.ValueRange))
	}
	return strings.Join(parts, "\n"), nil
}

func sheetColumnName(n int) string {
	var name string
	for n > 0 {
		n--
		name = string(rune('A'+n%26)) + name
		n /= 26
	}
	return name
}
