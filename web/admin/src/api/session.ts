import type { AdminUser } from "../types/admin";

const CSRF_KEY = "mainland-console-csrf";
const USER_KEY = "mainland-console-user";

export function getCSRFToken(): string {
  return window.localStorage.getItem(CSRF_KEY) ?? "";
}

export function setSession(csrfToken: string, user: AdminUser): void {
  window.localStorage.setItem(CSRF_KEY, csrfToken);
  window.localStorage.setItem(USER_KEY, JSON.stringify(user));
}

export function clearSession(): void {
  window.localStorage.removeItem(CSRF_KEY);
  window.localStorage.removeItem(USER_KEY);
}

export function getStoredUser(): AdminUser | null {
  const raw = window.localStorage.getItem(USER_KEY);
  if (!raw) return null;
  try {
    return JSON.parse(raw) as AdminUser;
  } catch {
    clearSession();
    return null;
  }
}
