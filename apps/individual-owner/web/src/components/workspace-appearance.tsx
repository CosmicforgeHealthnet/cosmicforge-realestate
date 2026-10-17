'use client';
import {useWorkspace} from '@/features/workspace/use-workspace';
export function WorkspaceAppearance({children}:{children:React.ReactNode}){const {data}=useWorkspace();const preference=data?.records.filter(r=>r.type==='preference').at(-1)?.data;return <div className={preference?.largeText?'owner-large-text':undefined} data-reduced-motion={preference?.reducedMotion?'true':undefined}>{children}</div>}
