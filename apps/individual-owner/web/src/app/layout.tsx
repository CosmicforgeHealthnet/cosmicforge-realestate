import type { Metadata } from 'next';
import { Providers } from './providers';
import { AppShell } from '@/components/app-shell';
import './globals.css';
export const metadata: Metadata = {title:'CosmicForge · Owner workspace demo',description:'Individual Owner mock foundation. Fictional records. No live services.',robots:{index:false,follow:false}};
export default function RootLayout({ children }: Readonly<{children:React.ReactNode}>) {
  return <html lang="en" suppressHydrationWarning><body><Providers><AppShell>{children}</AppShell></Providers></body></html>;
}
