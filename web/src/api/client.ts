const API_BASE = '/api/v1';

type UnauthorizedHandler = () => void;

class ApiClient {
  private csrfToken: string = '';
  private onUnauthorized?: UnauthorizedHandler;

  setCSRFToken(token: string) {
    this.csrfToken = token;
  }

  clearSession() {
    this.csrfToken = '';
  }

  setOnUnauthorized(handler: UnauthorizedHandler) {
    this.onUnauthorized = handler;
  }

  private headers(method: string): HeadersInit {
    const h: HeadersInit = { 'Content-Type': 'application/json' };
    if (!['GET', 'HEAD', 'OPTIONS'].includes(method) && this.csrfToken) h['X-CSRF-Token'] = this.csrfToken;
    return h;
  }

  async request<T = any>(method: string, path: string, body?: any, params?: Record<string, string>): Promise<T> {
    let url = `${API_BASE}${path}`;
    if (params) {
      const qs = new URLSearchParams(params).toString();
      if (qs) url += `?${qs}`;
    }
    const res = await fetch(url, {
      method,
      headers: this.headers(method),
      credentials: 'same-origin',
      body: body ? JSON.stringify(body) : undefined,
    });
    if (res.status === 401 && this.onUnauthorized) {
      this.onUnauthorized();
      throw new ApiError('Unauthorized', 401);
    }
    const json = await res.json();
    if (!json.ok) {
      throw new ApiError(json.error || 'Unknown error', res.status);
    }
    return json.data as T;
  }

  get<T = any>(path: string, params?: Record<string, string>) { return this.request<T>('GET', path, undefined, params); }
  post<T = any>(path: string, body?: any) { return this.request<T>('POST', path, body); }
  put<T = any>(path: string, body?: any) { return this.request<T>('PUT', path, body); }
  patch<T = any>(path: string, body?: any) { return this.request<T>('PATCH', path, body); }
  delete<T = any>(path: string) { return this.request<T>('DELETE', path); }

  /** Fetch raw text (non-JSON) from an API endpoint. */
  async raw(path: string): Promise<string> {
    const res = await fetch(`${API_BASE}${path}`, { credentials: 'same-origin' });
    if (res.status === 401 && this.onUnauthorized) {
      this.onUnauthorized();
      throw new ApiError('Unauthorized', 401);
    }
    if (!res.ok) throw new ApiError(res.statusText, res.status);
    return res.text();
  }

	async streamJSONLines<T>(path: string, onValue: (value: T) => void, signal?: AbortSignal): Promise<void> {
		const res = await fetch(`${API_BASE}${path}`, { credentials: 'same-origin', signal });
		if (res.status === 401 && this.onUnauthorized) {
			this.onUnauthorized();
			throw new ApiError('Unauthorized', 401);
		}
		if (!res.ok || !res.body) throw new ApiError(res.statusText, res.status);
		const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
		let pending = '';
		for (;;) {
			const { value, done } = await reader.read();
			pending += value || '';
			let newline = pending.indexOf('\n');
			while (newline >= 0) {
				const line = pending.slice(0, newline).trim();
				pending = pending.slice(newline + 1);
				if (line) onValue(JSON.parse(line) as T);
				newline = pending.indexOf('\n');
			}
			if (done) break;
		}
		if (pending.trim()) onValue(JSON.parse(pending) as T);
	}
}

export class ApiError extends Error {
  constructor(message: string, public status: number) {
    super(message);
    this.name = 'ApiError';
  }
}

export const api = new ApiClient();
export default api;
