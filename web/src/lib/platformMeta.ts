export interface FieldDef {
  key: string;
  labelKey: string;
  required?: boolean;
  type?: 'text' | 'password' | 'number' | 'boolean';
  placeholder?: string;
  hintKey?: string;
  group?: 'basic' | 'advanced';
}

export interface PlatformMeta {
  label: string;
  fields: FieldDef[];
}

export const platformMeta: Record<string, PlatformMeta> = {
  telegram: {
    label: 'Telegram',
    fields: [
      { key: 'token', labelKey: 'fields.botToken', required: true, type: 'password', placeholder: '123456:ABC-DEF...' },
      { key: 'allow_from', labelKey: 'fields.allowFrom', placeholder: '* (all)', group: 'advanced', hintKey: 'fields.allowFromHintTelegram' },
      { key: 'group_reply_all', labelKey: 'fields.groupReplyAll', type: 'boolean', group: 'advanced' },
      { key: 'share_session_in_channel', labelKey: 'fields.sharedGroupSession', type: 'boolean', group: 'advanced' },
    ],
  },
  discord: {
    label: 'Discord',
    fields: [
      { key: 'token', labelKey: 'fields.botToken', required: true, type: 'password' },
      { key: 'allow_from', labelKey: 'fields.allowFrom', placeholder: '* (all)', group: 'advanced' },
      { key: 'guild_id', labelKey: 'fields.guildId', placeholder: '', group: 'advanced', hintKey: 'fields.guildIdHint' },
      { key: 'group_reply_all', labelKey: 'fields.groupReplyAll', type: 'boolean', group: 'advanced' },
      { key: 'share_session_in_channel', labelKey: 'fields.sharedChannelSession', type: 'boolean', group: 'advanced' },
      { key: 'thread_isolation', labelKey: 'fields.threadIsolation', type: 'boolean', group: 'advanced' },
    ],
  },
  slack: {
    label: 'Slack',
    fields: [
      { key: 'bot_token', labelKey: 'fields.botToken', required: true, type: 'password', placeholder: 'xoxb-...' },
      { key: 'app_token', labelKey: 'fields.appToken', required: true, type: 'password', placeholder: 'xapp-...' },
      { key: 'allow_from', labelKey: 'fields.allowFrom', placeholder: '* (all)', group: 'advanced' },
      { key: 'share_session_in_channel', labelKey: 'fields.sharedChannelSession', type: 'boolean', group: 'advanced' },
    ],
  },
  dingtalk: {
    label: 'DingTalk',
    fields: [
      { key: 'client_id', labelKey: 'fields.clientId', required: true },
      { key: 'client_secret', labelKey: 'fields.clientSecret', required: true, type: 'password' },
      { key: 'allow_from', labelKey: 'fields.allowFrom', placeholder: '* (all)', group: 'advanced' },
      { key: 'share_session_in_channel', labelKey: 'fields.sharedGroupSession', type: 'boolean', group: 'advanced' },
    ],
  },
  wecom: {
    label: 'WeChat Work',
    fields: [
      { key: 'corp_id', labelKey: 'fields.corpId', required: true },
      { key: 'corp_secret', labelKey: 'fields.corpSecret', required: true, type: 'password' },
      { key: 'agent_id', labelKey: 'fields.agentId', required: true, placeholder: '1000002' },
      { key: 'callback_token', labelKey: 'fields.callbackToken', required: true },
      { key: 'callback_aes_key', labelKey: 'fields.callbackAesKey', required: true, hintKey: 'fields.callbackAesKeyHint' },
      { key: 'port', labelKey: 'fields.port', required: true, placeholder: '8081' },
      { key: 'callback_path', labelKey: 'fields.callbackPath', placeholder: '/wecom/callback', group: 'advanced' },
      { key: 'api_base_url', labelKey: 'fields.apiBaseUrl', placeholder: 'https://qyapi.weixin.qq.com', group: 'advanced' },
      { key: 'allow_from', labelKey: 'fields.allowFrom', placeholder: '* (all)', group: 'advanced' },
    ],
  },
  qq: {
    label: 'QQ (OneBot v11)',
    fields: [
      { key: 'ws_url', labelKey: 'fields.wsUrl', required: true, placeholder: 'ws://127.0.0.1:3001' },
      { key: 'token', labelKey: 'fields.accessToken', type: 'password', group: 'advanced' },
      { key: 'allow_from', labelKey: 'fields.allowFrom', placeholder: '* (all)', group: 'advanced' },
      { key: 'share_session_in_channel', labelKey: 'fields.sharedGroupSession', type: 'boolean', group: 'advanced' },
    ],
  },
  qqbot: {
    label: 'QQ Bot (Official)',
    fields: [
      { key: 'app_id', labelKey: 'fields.appId', required: true },
      { key: 'app_secret', labelKey: 'fields.appSecret', required: true, type: 'password' },
      { key: 'sandbox', labelKey: 'fields.sandboxMode', type: 'boolean', group: 'advanced' },
      { key: 'allow_from', labelKey: 'fields.allowFrom', placeholder: '* (all)', group: 'advanced' },
      { key: 'share_session_in_channel', labelKey: 'fields.sharedGroupSession', type: 'boolean', group: 'advanced' },
    ],
  },
  line: {
    label: 'LINE',
    fields: [
      { key: 'channel_secret', labelKey: 'fields.channelSecret', required: true, type: 'password' },
      { key: 'channel_token', labelKey: 'fields.channelToken', required: true, type: 'password' },
      { key: 'port', labelKey: 'fields.port', required: true, placeholder: '8080' },
      { key: 'callback_path', labelKey: 'fields.callbackPath', placeholder: '/callback', group: 'advanced' },
      { key: 'allow_from', labelKey: 'fields.allowFrom', placeholder: '* (all)', group: 'advanced' },
    ],
  },
  weibo: {
    label: 'Weibo (微博)',
    fields: [
      { key: 'app_id', labelKey: 'fields.appId', required: true, placeholder: '1234567890' },
      { key: 'app_secret', labelKey: 'fields.appSecret', required: true, type: 'password' },
      { key: 'allow_from', labelKey: 'fields.allowFrom', placeholder: '* (all)', group: 'advanced' },
    ],
  },
};
