'use client';
import { createContext, useContext, useSyncExternalStore } from 'react';
const ThemeContext = createContext({ dark: false, toggle: () => {} });
const key = 'cosmicforge.theme.v1';
let memoryTheme: boolean | undefined;
const subscribe = (listener: () => void) => {
  window.addEventListener('storage', listener); window.addEventListener('owner-theme', listener);
  const media = window.matchMedia('(prefers-color-scheme: dark)'); media.addEventListener('change', listener);
  return () => { window.removeEventListener('storage', listener); window.removeEventListener('owner-theme', listener); media.removeEventListener('change', listener); };
};
const snapshot = () => {
  try { const value = localStorage.getItem(key); if (value === 'dark' || value === 'light') return value === 'dark'; } catch {}
  return memoryTheme ?? window.matchMedia('(prefers-color-scheme: dark)').matches;
};
export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const dark = useSyncExternalStore(subscribe, snapshot, () => false);
  function toggle() {
    memoryTheme = !dark;
    try { localStorage.setItem(key, dark ? 'light' : 'dark'); } catch {}
    window.dispatchEvent(new Event('owner-theme'));
  }
  return <ThemeContext value={{dark,toggle}}><div className="theme-root" data-theme={dark ? 'dark' : 'light'}>{children}</div></ThemeContext>;
}
export const useTheme = () => useContext(ThemeContext);
