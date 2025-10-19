import { AxiosInstance } from 'axios';
import { User, UserProfileResponse, HeimdallError } from './types';

export class UserModule {
  private client: AxiosInstance;

  constructor(client: AxiosInstance) {
    this.client = client;
  }

  async getProfile(): Promise<User> {
    try {
      const response = await this.client.get<UserProfileResponse>('/v1/users/me');

      if (!response.data.success || !response.data.data) {
        throw new HeimdallError(
          response.data.error?.message || 'Failed to get profile',
          response.data.error?.code || 'PROFILE_FETCH_FAILED',
          response.status
        );
      }

      return response.data.data;
    } catch (error: any) {
      if (error.response) {
        const errorData = error.response.data;
        throw new HeimdallError(
          errorData.error?.message || 'Failed to get profile',
          errorData.error?.code || 'PROFILE_FETCH_FAILED',
          error.response.status
        );
      }
      throw error;
    }
  }

  async updateProfile(updates: {
    firstName?: string;
    lastName?: string;
    metadata?: Record<string, any>;
  }): Promise<User> {
    try {
      const response = await this.client.patch<UserProfileResponse>('/v1/users/me', updates);

      if (!response.data.success || !response.data.data) {
        throw new HeimdallError(
          response.data.error?.message || 'Failed to update profile',
          response.data.error?.code || 'PROFILE_UPDATE_FAILED',
          response.status
        );
      }

      return response.data.data;
    } catch (error: any) {
      if (error.response) {
        const errorData = error.response.data;
        throw new HeimdallError(
          errorData.error?.message || 'Failed to update profile',
          errorData.error?.code || 'PROFILE_UPDATE_FAILED',
          error.response.status
        );
      }
      throw error;
    }
  }

  async deleteAccount(): Promise<void> {
    try {
      await this.client.delete('/v1/users/me');
    } catch (error: any) {
      if (error.response) {
        const errorData = error.response.data;
        throw new HeimdallError(
          errorData.error?.message || 'Failed to delete account',
          errorData.error?.code || 'ACCOUNT_DELETE_FAILED',
          error.response.status
        );
      }
      throw error;
    }
  }
}
