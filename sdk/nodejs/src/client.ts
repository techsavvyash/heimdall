import axios, { AxiosInstance, AxiosRequestConfig } from 'axios';
import { HeimdallConfig, HeimdallError } from './types';
import { AuthModule } from './auth';
import { UserModule } from './user';
import { getDefaultStorage } from './storage';

export class HeimdallClient {
  private client: AxiosInstance;
  public auth: AuthModule;
  public user: UserModule;
  private config: HeimdallConfig;

  constructor(config: HeimdallConfig) {
    this.config = {
      storage: getDefaultStorage(),
      autoRefresh: true,
      refreshBuffer: 60,
      ...config
    };

    // Create axios instance
    this.client = axios.create({
      baseURL: this.config.apiUrl,
      headers: {
        'Content-Type': 'application/json',
        ...this.config.headers
      }
    });

    // Initialize modules
    this.auth = new AuthModule(
      this.client,
      this.config.storage!,
      this.config.autoRefresh,
      this.config.refreshBuffer,
      this.config.onTokenRefresh
    );

    this.user = new UserModule(this.client);

    // Setup request interceptor to add auth token
    this.client.interceptors.request.use(
      async (config) => {
        const token = await this.auth.getAccessToken();
        if (token) {
          config.headers.Authorization = `Bearer ${token}`;
        }

        // Add tenant ID if configured
        if (this.config.tenantId) {
          config.headers['X-Tenant-ID'] = this.config.tenantId;
        }

        return config;
      },
      (error) => {
        return Promise.reject(error);
      }
    );

    // Setup response interceptor to handle errors
    this.client.interceptors.response.use(
      (response) => response,
      async (error) => {
        const originalRequest = error.config as AxiosRequestConfig & { _retry?: boolean };

        // If 401 and not already retried, try to refresh token
        if (error.response?.status === 401 && !originalRequest._retry) {
          originalRequest._retry = true;

          try {
            await this.auth.refreshTokens();
            return this.client(originalRequest);
          } catch (refreshError) {
            // If refresh fails, call onAuthError callback
            if (this.config.onAuthError) {
              this.config.onAuthError(refreshError as Error);
            }
            return Promise.reject(refreshError);
          }
        }

        return Promise.reject(error);
      }
    );
  }

  /**
   * Make a custom API request
   */
  async request<T = any>(config: AxiosRequestConfig): Promise<T> {
    try {
      const response = await this.client.request<T>(config);
      return response.data;
    } catch (error: any) {
      if (error.response) {
        const errorData = error.response.data;
        throw new HeimdallError(
          errorData.error?.message || 'Request failed',
          errorData.error?.code || 'REQUEST_FAILED',
          error.response.status
        );
      }
      throw error;
    }
  }

  /**
   * Check if user is authenticated
   */
  async isAuthenticated(): Promise<boolean> {
    return this.auth.isAuthenticated();
  }
}
