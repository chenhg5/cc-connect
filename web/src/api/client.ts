const API_BASE = '/api/v1';

type UnauthorizedHandler = () => void;

class ApiClient {
  private token: string = '';
  private onUnauthorized?: UnauthorizedHandler;

  setToken(token: string) {
    this.token = token;
  }

  getToken(): string {
    return this.token;
  }

  setOnUnauthorized(handler: UnauthorizedHandler) {
    this.onUnauthorized = handler;
  }

  private headers(): HeadersInit {
    const h: HeadersInit = { 'Content-Type': 'application/json' };
    if (this.token) h['Authorization'] = `Bearer ${this.token}`;
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
      headers: this.headers(),
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
    const h: HeadersInit = {};
    if (this.token) h['Authorization'] = `Bearer ${this.token}`;
    const res = await fetch(`${API_BASE}${path}`, { headers: h });
    if (res.status === 401 && this.onUnauthorized) {
      this.onUnauthorized();
      throw new ApiError('Unauthorized', 401);
    }
    if (!res.ok) throw new ApiError(res.statusText, res.status);
    return res.text();
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
