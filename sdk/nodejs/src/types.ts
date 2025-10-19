export interface HeimdallConfig {
  apiUrl: string;
  tenantId?: string;
  storage?: Storage;
  autoRefresh?: boolean;
  refreshBuffer?: number;
  onTokenRefresh?: (tokens: TokenPair) => void;
  onAuthError?: (error: Error) => void;
  headers?: Record<string, string>;
}

export interface TokenPair {
  accessToken: string;
  refreshToken: string;
  expiresIn: number;
}

export interface User {
  id: string;
  email: string;
  firstName?: string;
  lastName?: string;
  tenantId: string;
  metadata?: Record<string, any>;
  roles?: string[];
  loginCount?: number;
  createdAt: string;
}

export interface RegisterRequest {
  email: string;
  password: string;
  firstName?: string;
  lastName?: string;
  tenantId?: string;
  metadata?: Record<string, any>;
}

export interface LoginRequest {
  email: string;
  password: string;
  rememberMe?: boolean;
}

export interface AuthResponse {
  success: boolean;
  data?: {
    user: User;
    accessToken: string;
    refreshToken: string;
    expiresIn: number;
  };
  error?: {
    code: string;
    message: string;
  };
}

export interface UserProfileResponse {
  success: boolean;
  data?: User;
  error?: {
    code: string;
    message: string;
  };
}

export interface Storage {
  getItem(key: string): string | null | Promise<string | null>;
  setItem(key: string, value: string): void | Promise<void>;
  removeItem(key: string): void | Promise<void>;
}

export class HeimdallError extends Error {
  code: string;
  statusCode?: number;

  constructor(message: string, code: string, statusCode?: number) {
    super(message);
    this.name = 'HeimdallError';
    this.code = code;
    this.statusCode = statusCode;
  }
}
