'use client';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useRepository } from '@/app/providers';
import type { Scenario } from '@/lib/contracts/owner';
export function useDashboard(scenario: Scenario = 'populated') {
  const repository = useRepository(); const client = useQueryClient();
  const query = useQuery({queryKey:['dashboard',scenario],queryFn:()=>repository.getDashboard(scenario)});
  const refresh = () => client.invalidateQueries({queryKey:['dashboard']});
  const task = useMutation({mutationFn:({id,done}:{id:string;done:boolean})=>repository.setTaskDone(id,done),onSuccess:refresh});
  const rename = useMutation({mutationFn:(name:string)=>repository.renameWorkspace(name),onSuccess:refresh});
  return { ...query, task, rename };
}
