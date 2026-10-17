'use client';
import { createContext, useContext, useState } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MockOwnerRepository } from '@/lib/mock/owner-repository';
import type { OwnerRepository } from '@/lib/repositories/owner-repository';
import { ThemeProvider } from '@/design/theme-provider';
import {WorkspaceAppearance} from '@/components/workspace-appearance';
import {DraftNavigationProvider} from '@/components/workspace-ui';
import { ScopeProvider } from '@/components/workspace-tools';
const RepositoryContext = createContext<OwnerRepository | null>(null);
export function Providers({ children }: { children: React.ReactNode }) {
  const [client] = useState(() => new QueryClient({defaultOptions:{queries:{retry:false,staleTime:Infinity}}}));
  const [repository] = useState<OwnerRepository>(() => new MockOwnerRepository());
  return <DraftNavigationProvider><RepositoryContext value={repository}><QueryClientProvider client={client}><ThemeProvider><ScopeProvider><WorkspaceAppearance>{children}</WorkspaceAppearance></ScopeProvider></ThemeProvider></QueryClientProvider></RepositoryContext></DraftNavigationProvider>;
}
export function useRepository() {
  const repository = useContext(RepositoryContext);
  if (!repository) throw new Error('Repository provider missing.');
  return repository;
}
