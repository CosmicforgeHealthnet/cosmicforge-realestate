import {Suspense} from 'react';
import {WorkspaceScreen} from '@/features/workspace/workspace-screen';
import {LoadingState} from '@/components/states';
export default async function RecordPage({params}:{params:Promise<{module:string;record:string[]}>}){const {module,record}=await params;return <Suspense fallback={<LoadingState/>}><WorkspaceScreen slug={module} segments={record}/></Suspense>}
