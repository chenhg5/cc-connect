package core

const MsgReasoningSaveFailed MsgKey = "reasoning_save_failed"

func init() {
	messages[MsgReasoningSaveFailed] = map[Language]string{
		LangEnglish:            "❌ Failed to save reasoning effort: %s",
		LangChinese:            "❌ 保存推理强度失败: %s",
		LangTraditionalChinese: "❌ 儲存推理強度失敗: %s",
		LangJapanese:           "❌ 推論強度の保存に失敗しました: %s",
		LangSpanish:            "❌ No se pudo guardar el esfuerzo de razonamiento: %s",
	}
}
