package core

const MsgDeleteInUseDenied MsgKey = "delete_in_use_denied"

func init() {
	messages[MsgDeleteInUseDenied] = map[Language]string{
		LangEnglish:            "❌ Cannot delete %s: it is the active session of another chat.",
		LangChinese:            "❌ 不能删除 %s：它是其他对话正在使用的会话。",
		LangTraditionalChinese: "❌ 不能刪除 %s：它是其他對話正在使用的會話。",
		LangJapanese:           "❌ %s は削除できません：別のチャットで使用中のセッションです。",
		LangSpanish:            "❌ No se puede eliminar %s: es la sesión activa de otro chat.",
	}
}
