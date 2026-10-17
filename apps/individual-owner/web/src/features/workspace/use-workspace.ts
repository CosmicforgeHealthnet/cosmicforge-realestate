'use client';
import {useHydrated} from '@/components/use-hydrated';
import { useMutation,useQuery,useQueryClient } from '@tanstack/react-query';
import { useRepository } from '@/app/providers';
import type {WorkspaceCommand,WorkspaceSchema,WorkspaceState} from '@/lib/contracts/workspace';
import definition from '../../../../shared/workflows.json';
import { WorkspaceEngine } from '@/lib/mock/workspace-engine';
export const workspaceSchema=definition as WorkspaceSchema;
export function recordPath(type:string,id:string){const spec=workspaceSchema.types.find(t=>t.id===type);return `/workspace/${spec?.module??'properties'}/${type}/${id}`}
export function deriveWorkspace(state:WorkspaceState){const engine=new WorkspaceEngine(workspaceSchema,state);return {summary:(property='all',start?:string,end?:string)=>engine.summary(property,start,end),propertyOf:(r:WorkspaceState['records'][number])=>engine.propertyOf(r)}}
export function useWorkspace(){const hydrated=useHydrated(),repository=useRepository(),client=useQueryClient();const query=useQuery({queryKey:['workspace'],queryFn:()=>repository.readWorkspace()});
 const refresh=()=>client.invalidateQueries();
 const command=useMutation({mutationFn:(input:WorkspaceCommand)=>repository.execute(input),onSuccess:refresh});
 const scenario=useMutation({mutationFn:(value:string)=>repository.setScenario(value),onSuccess:refresh});
 const actor=useMutation({mutationFn:(value:string)=>repository.setActor(value),onSuccess:refresh});
 const reset=useMutation({mutationFn:()=>repository.resetDemo(),onSuccess:refresh});
 return {...query,data:hydrated?query.data:undefined,isPending:!hydrated||query.isPending,command,scenario,actor,reset};
}
