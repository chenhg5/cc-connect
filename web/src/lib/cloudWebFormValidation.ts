export type CloudWebValidationIssue = {
  messageKey: string;
  fieldLabelKey?: string;
};

function str(v: unknown): string {
  return typeof v === 'string' ? v.trim() : '';
}

/** Accepts http(s) URLs or bare host[:port] (matches backend joinURL / resolveWSURL). */
export function isHttpBaseUrl(value: string): boolean {
  const trimmed = value.trim();
  if (!trimmed) return false;
  if (trimmed.includes('://')) {
    try {
      const u = new URL(trimmed);
      return u.protocol === 'http:' || u.protocol === 'https:';
    } catch {
      return false;
    }
  }
  // host, host:port, or IPv6 in brackets
  return /^(\[[\da-f:]+\]|[^\s/]+?)(:\d+)?$/i.test(trimmed);
}

export function isWsUrl(value: string): boolean {
  const trimmed = value.trim();
  if (!trimmed) return false;
  try {
    const u = new URL(trimmed);
    return u.protocol === 'ws:' || u.protocol === 'wss:';
  } catch {
    return false;
  }
}

export function isAbsoluteHttpUrl(value: string): boolean {
  const trimmed = value.trim();
  if (!trimmed) return false;
  try {
    const u = new URL(trimmed);
    return u.protocol === 'http:' || u.protocol === 'https:';
  } catch {
    return false;
  }
}

export function validateCloudWebForm(values: Record<string, unknown>): CloudWebValidationIssue | null {
  const transport = str(values.transport) || 'websocket';
  const baseUrl = str(values.base_url);
  const wsUrl = str(values.ws_url);
  const registerUrl = str(values.register_url);
  const publicUrl = str(values.public_url);

  switch (transport) {
    case 'websocket':
      if (!baseUrl && !wsUrl) {
        return { messageKey: 'setup.cloudWeb.urlRequiredWebsocket' };
      }
      if (baseUrl && !isHttpBaseUrl(baseUrl)) {
        return { messageKey: 'setup.cloudWeb.invalidHttpUrl', fieldLabelKey: 'fields.apiBaseUrl' };
      }
      if (wsUrl && !isWsUrl(wsUrl)) {
        return { messageKey: 'setup.cloudWeb.invalidWsUrl' };
      }
      break;

    case 'long_poll':
      if (!baseUrl) {
        return { messageKey: 'setup.cloudWeb.urlRequiredLongPoll' };
      }
      if (!isHttpBaseUrl(baseUrl)) {
        return { messageKey: 'setup.cloudWeb.invalidHttpUrl', fieldLabelKey: 'fields.apiBaseUrl' };
      }
      break;

    case 'gateway':
      if (!baseUrl && !registerUrl) {
        return { messageKey: 'setup.cloudWeb.urlRequiredGateway' };
      }
      // Mirror backend New(): register_url requires public_url so the gateway
      // can reach the cc-connect webhook callback.
      if (registerUrl && !publicUrl) {
        return { messageKey: 'setup.cloudWeb.publicUrlRequiredGateway' };
      }
      if (baseUrl && !isHttpBaseUrl(baseUrl)) {
        return { messageKey: 'setup.cloudWeb.invalidHttpUrl', fieldLabelKey: 'fields.apiBaseUrl' };
      }
      if (registerUrl && !isAbsoluteHttpUrl(registerUrl)) {
        return { messageKey: 'setup.cloudWeb.invalidHttpUrl', fieldLabelKey: 'fields.registerUrl' };
      }
      if (publicUrl && !isAbsoluteHttpUrl(publicUrl)) {
        return { messageKey: 'setup.cloudWeb.invalidHttpUrl', fieldLabelKey: 'fields.publicUrl' };
      }
      break;

    default:
      break;
  }

  return null;
}
