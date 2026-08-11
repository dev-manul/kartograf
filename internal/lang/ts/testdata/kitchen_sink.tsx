import React from 'react';
import { Button, Text as UIText } from '@stripcash/ui-kit';
import * as api from './api/client';
import { helper } from '../utils';

/** Retry limit. */
export const MAX_RETRIES = 3;
let counter = 0;

export type UserId = number;

/** User persistence. */
export interface UserRepo {
  find(id: UserId): Promise<User>;
  name: string;
}

export interface AdminRepo extends UserRepo {}

export enum Status {
  Active = 'active',
  Banned = 'banned',
}

export abstract class BaseService {}

/** Handles users. */
export class UserService extends BaseService implements UserRepo {
  private repo: UserRepo;
  name = 'svc';

  handleRefresh = () => {
    this.load();
  };

  constructor(private readonly client: HttpClient) {
    super();
  }

  /** Finds a user. */
  async find(id: UserId): Promise<User> {
    const u = await this.repo.find(id);
    this.log(u);
    api.fetchUser(id);
    helper(1);
    const b = new Button();
    this.client.get('/users');
    return u;
  }

  private log(u: User): void {}
}

export function formatUser(u: UserService): string {
  return u.find(1);
}

export const Card = ({ user }: CardProps) => (
  <div className="card">
    <Button onClick={() => api.fetchUser(1)} />
    <UIText>{user.name}</UIText>
  </div>
);
