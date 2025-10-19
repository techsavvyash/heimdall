// Browser-compatible Heimdall SDK
class HeimdallClient {
  constructor(config) {
    this.apiUrl = config.apiUrl;
    this.tenantId = config.tenantId;
    this.storage = window.localStorage;

    this.auth = {
      register: this.register.bind(this),
      login: this.login.bind(this),
      logout: this.logout.bind(this),
      refreshTokens: this.refreshTokens.bind(this),
      isAuthenticated: this.isAuthenticated.bind(this)
    };

    this.user = {
      getProfile: this.getProfile.bind(this),
      updateProfile: this.updateProfile.bind(this),
      deleteAccount: this.deleteAccount.bind(this)
    };
  }

  async request(path, options = {}) {
    const url = `${this.apiUrl}${path}`;
    const headers = {
      'Content-Type': 'application/json',
      ...options.headers
    };

    // Add auth token if available
    const token = this.storage.getItem('heimdall_access_token');
    if (token) {
      headers['Authorization'] = `Bearer ${token}`;
    }

    // Add tenant ID if configured
    if (this.tenantId) {
      headers['X-Tenant-ID'] = this.tenantId;
    }

    const response = await fetch(url, {
      ...options,
      headers,
      body: options.body ? JSON.stringify(options.body) : undefined
    });

    const data = await response.json();

    if (!data.success) {
      const error = new Error(data.error?.message || 'Request failed');
      error.code = data.error?.code || 'REQUEST_FAILED';
      error.statusCode = response.status;
      throw error;
    }

    return data.data;
  }

  async register(request) {
    const data = await this.request('/v1/auth/register', {
      method: 'POST',
      body: request
    });

    // Store tokens
    this.storeTokens(data);
    return data.user;
  }

  async login(request) {
    const data = await this.request('/v1/auth/login', {
      method: 'POST',
      body: request
    });

    // Store tokens
    this.storeTokens(data);
    return data.user;
  }

  async logout() {
    try {
      await this.request('/v1/auth/logout', {
        method: 'POST'
      });
    } catch (error) {
      // Ignore errors on logout
    } finally {
      this.clearTokens();
    }
  }

  async refreshTokens() {
    const refreshToken = this.storage.getItem('heimdall_refresh_token');

    if (!refreshToken) {
      throw new Error('No refresh token available');
    }

    const data = await this.request('/v1/auth/refresh', {
      method: 'POST',
      body: { refreshToken }
    });

    this.storeTokens(data);
    return data;
  }

  async getProfile() {
    return await this.request('/v1/users/me', {
      method: 'GET'
    });
  }

  async updateProfile(updates) {
    return await this.request('/v1/users/me', {
      method: 'PATCH',
      body: updates
    });
  }

  async deleteAccount() {
    await this.request('/v1/users/me', {
      method: 'DELETE'
    });
  }

  async isAuthenticated() {
    const token = this.storage.getItem('heimdall_access_token');
    if (!token) return false;

    const expiresAt = this.storage.getItem('heimdall_expires_at');
    if (!expiresAt) return false;

    return Date.now() < parseInt(expiresAt);
  }

  storeTokens(data) {
    this.storage.setItem('heimdall_access_token', data.accessToken);
    this.storage.setItem('heimdall_refresh_token', data.refreshToken);

    const expiresAt = Date.now() + (data.expiresIn * 1000);
    this.storage.setItem('heimdall_expires_at', expiresAt.toString());
  }

  clearTokens() {
    this.storage.removeItem('heimdall_access_token');
    this.storage.removeItem('heimdall_refresh_token');
    this.storage.removeItem('heimdall_expires_at');
  }
}

// Export for browser usage
window.HeimdallClient = HeimdallClient;
