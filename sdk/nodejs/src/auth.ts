import axios, { AxiosInstance } from 'axios';
import {
  RegisterRequest,
  LoginRequest,
  AuthResponse,
  TokenPair,
  HeimdallError,
  Storage,
  User
} from './types';

const ACCESS_TOKEN_KEY = 'heimdall_access_token';
const REFRESH_TOKEN_KEY = 'heimdall_refresh_token';
const EXPIRES_AT_KEY = 'heimdall_expires_at';

export class AuthModule {
  private client: AxiosInstance;
  private storage: Storage;
  private autoRefresh: boolean;
  private refreshBuffer: number;
  private onTokenRefresh?: (tokens: TokenPair) => void;
  private refreshTimer?: NodeJS.Timeout;

  constructor(
    client: AxiosInstance,
    storage: Storage,
    autoRefresh: boolean = true,
    refreshBuffer: number = 60,
    onTokenRefresh?: (tokens: TokenPair) => void
  ) {
    this.client = client;
    this.storage = storage;
    this.autoRefresh = autoRefresh;
    this.refreshBuffer = refreshBuffer;
    this.onTokenRefresh = onTokenRefresh;
  }

  async register(request: RegisterRequest): Promise<User> {
    try {
      const response = await this.client.post<AuthResponse>('/v1/auth/register', request);

      if (!response.data.success || !response.data.data) {
        throw new HeimdallError(
          response.data.error?.message || 'Registration failed',
          response.data.error?.code || 'REGISTRATION_FAILED',
          response.status
        );
      }

      const { user, accessToken, refreshToken, expiresIn } = response.data.data;

      // Store tokens
      await this.storeTokens({ accessToken, refreshToken, expiresIn });

      // Setup auto-refresh
      if (this.autoRefresh) {
        this.scheduleTokenRefresh(expiresIn);
      }

      return user;
    } catch (error: any) {
      if (error.response) {
        const errorData = error.response.data;
        throw new HeimdallError(
          errorData.error?.message || 'Registration failed',
          errorData.error?.code || 'REGISTRATION_FAILED',
          error.response.status
        );
      }
      throw error;
    }
  }

  async login(request: LoginRequest): Promise<User> {
    try {
      const response = await this.client.post<AuthResponse>('/v1/auth/login', request);

      if (!response.data.success || !response.data.data) {
        throw new HeimdallError(
          response.data.error?.message || 'Login failed',
          response.data.error?.code || 'LOGIN_FAILED',
          response.status
        );
      }

      const { user, accessToken, refreshToken, expiresIn } = response.data.data;

      // Store tokens
      await this.storeTokens({ accessToken, refreshToken, expiresIn });

      // Setup auto-refresh
      if (this.autoRefresh) {
        this.scheduleTokenRefresh(expiresIn);
      }

      return user;
    } catch (error: any) {
      if (error.response) {
        const errorData = error.response.data;
        throw new HeimdallError(
          errorData.error?.message || 'Login failed',
          errorData.error?.code || 'LOGIN_FAILED',
          error.response.status
        );
      }
      throw error;
    }
  }

  async logout(): Promise<void> {
    try {
      await this.client.post('/v1/auth/logout');
    } catch (error) {
      // Ignore errors on logout
    } finally {
      await this.clearTokens();
      if (this.refreshTimer) {
        clearTimeout(this.refreshTimer);
      }
    }
  }

  async refreshTokens(): Promise<TokenPair> {
    const refreshToken = await this.storage.getItem(REFRESH_TOKEN_KEY);

    if (!refreshToken) {
      throw new HeimdallError('No refresh token available', 'NO_REFRESH_TOKEN');
    }

    try {
      const response = await this.client.post<AuthResponse>('/v1/auth/refresh', {
        refreshToken
      });

      if (!response.data.success || !response.data.data) {
        throw new HeimdallError(
          response.data.error?.message || 'Token refresh failed',
          response.data.error?.code || 'REFRESH_FAILED',
          response.status
        );
      }

      const { accessToken, refreshToken: newRefreshToken, expiresIn } = response.data.data;
      const tokens = { accessToken, refreshToken: newRefreshToken, expiresIn };

      await this.storeTokens(tokens);

      if (this.onTokenRefresh) {
        this.onTokenRefresh(tokens);
      }

      if (this.autoRefresh) {
        this.scheduleTokenRefresh(expiresIn);
      }

      return tokens;
    } catch (error: any) {
      await this.clearTokens();
      if (error.response) {
        const errorData = error.response.data;
        throw new HeimdallError(
          errorData.error?.message || 'Token refresh failed',
          errorData.error?.code || 'REFRESH_FAILED',
          error.response.status
        );
      }
      throw error;
    }
  }

  async getAccessToken(): Promise<string | null> {
    return await this.storage.getItem(ACCESS_TOKEN_KEY);
  }

  async isAuthenticated(): Promise<boolean> {
    const token = await this.getAccessToken();
    if (!token) return false;

    const expiresAt = await this.storage.getItem(EXPIRES_AT_KEY);
    if (!expiresAt) return false;

    return Date.now() < parseInt(expiresAt);
  }

  private async storeTokens(tokens: TokenPair): Promise<void> {
    await this.storage.setItem(ACCESS_TOKEN_KEY, tokens.accessToken);
    await this.storage.setItem(REFRESH_TOKEN_KEY, tokens.refreshToken);

    const expiresAt = Date.now() + tokens.expiresIn * 1000;
    await this.storage.setItem(EXPIRES_AT_KEY, expiresAt.toString());
  }

  private async clearTokens(): Promise<void> {
    await this.storage.removeItem(ACCESS_TOKEN_KEY);
    await this.storage.removeItem(REFRESH_TOKEN_KEY);
    await this.storage.removeItem(EXPIRES_AT_KEY);
  }

  private scheduleTokenRefresh(expiresIn: number): void {
    if (this.refreshTimer) {
      clearTimeout(this.refreshTimer);
    }

    // Schedule refresh before token expires (using refreshBuffer)
    const refreshIn = (expiresIn - this.refreshBuffer) * 1000;

    if (refreshIn > 0) {
      this.refreshTimer = setTimeout(async () => {
        try {
          await this.refreshTokens();
        } catch (error) {
          console.error('Auto-refresh failed:', error);
        }
      }, refreshIn);
    }
  }
}
