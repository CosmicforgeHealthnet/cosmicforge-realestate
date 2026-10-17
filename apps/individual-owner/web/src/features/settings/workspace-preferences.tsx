'use client';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { useDashboard } from '@/features/dashboard/use-dashboard';
const schema = z.object({name:z.string().trim().min(2,'Use at least 2 characters.').max(60,'Use at most 60 characters.')});
export function WorkspacePreferences() {
  const {data,rename} = useDashboard();
  const {register,handleSubmit,formState:{errors}} = useForm<z.infer<typeof schema>>({resolver:zodResolver(schema)});
  return <section className="card preferences"><h2>Demo workspace preference</h2><p>Change the label shown above your portfolio. This stays in memory for this session.</p><p>Current label: <strong>{data?.workspaceName ?? 'Loading…'}</strong></p><form onSubmit={handleSubmit(v=>rename.mutate(v.name))} noValidate><label htmlFor="workspace-name">Workspace label</label><input id="workspace-name" {...register('name')} aria-invalid={Boolean(errors.name)} aria-describedby="workspace-help" placeholder="My demonstration portfolio" maxLength={60}/><p id="workspace-help" role={errors.name?'alert':undefined}>{errors.name?.message ?? '2–60 characters. Use fictional information.'}</p><button className="primary" disabled={rename.isPending}>Save demo preference</button><p role="status">{rename.isSuccess?'Demo preference saved locally.':rename.error?.message}</p></form></section>;
}
