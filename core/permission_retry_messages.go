package core

const (
	MsgPermissionStale      MsgKey = "permission_stale"
	MsgPermissionSubmitting MsgKey = "permission_submitting"
)

func init() {
	messages[MsgPermissionStale] = map[Language]string{
		LangEnglish:            "⚠️ This request is no longer active.",
		LangChinese:            "⚠️ 这个请求已失效。",
		LangTraditionalChinese: "⚠️ 這個請求已失效。",
		LangJapanese:           "⚠️ このリクエストは無効になっています。",
		LangSpanish:            "⚠️ Esta solicitud ya no está activa.",
	}
	messages[MsgPermissionSubmitting] = map[Language]string{
		LangEnglish:            "⏳ A response to this request is already being sent. Please wait for its result.",
		LangChinese:            "⏳ 正在发送这个请求的回复，请等待结果。",
		LangTraditionalChinese: "⏳ 正在傳送這個請求的回覆，請等待結果。",
		LangJapanese:           "⏳ このリクエストへの回答を送信中です。結果をお待ちください。",
		LangSpanish:            "⏳ Ya se está enviando una respuesta a esta solicitud. Espera el resultado.",
	}
}
