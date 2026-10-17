import { notFound } from 'next/navigation';
import { redirect } from 'next/navigation';
import modules from '../../../../../shared/modules.json';
import {Suspense} from 'react';
import {WorkspaceScreen} from '@/features/workspace/workspace-screen';
import {LoadingState} from '@/components/states';
export function generateStaticParams() { return modules.map(m=>({module:m.slug})); }
export default async function ModulePage({params}:{params:Promise<{module:string}>}) {
  const {module:slug} = await params; const area = modules.find(m=>m.slug===slug);
  if (!area) notFound();
  if (slug === 'dashboard') redirect('/');
  return <Suspense fallback={<LoadingState/>}><WorkspaceScreen slug={slug}/></Suspense>;
}
