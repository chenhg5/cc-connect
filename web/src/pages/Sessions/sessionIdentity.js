/** @param {{user_name?: string, user_id?: string, chat_name?: string, chat_id?: string}} session */
export function sessionOwnerLabel(session) {
  return [session.chat_name || session.chat_id, session.user_name || session.user_id]
    .filter(Boolean)
    .join(' · ');
}
