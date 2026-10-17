import { describe, it, expect, vi } from 'vitest';
import { MockOwnerRepository } from '@/lib/mock/owner-repository';
describe('mock owner repository',()=>{
  it('keeps fixtures deterministic and instances isolated without HTTP',async()=>{
    const request=vi.spyOn(globalThis,'fetch');
    const a=new MockOwnerRepository(),b=new MockOwnerRepository();
    const snapshot=await a.getDashboard('populated');snapshot.properties.length=0;
    expect((await a.getDashboard('populated')).properties).toHaveLength(3);
    await a.setTaskDone('demo-task-01',true);
    expect((await a.getDashboard('populated')).tasks[0].done).toBe(true);
    expect((await b.getDashboard('populated')).tasks[0].done).toBe(false);
    expect(request).not.toHaveBeenCalled();request.mockRestore();
  });
  it('supports empty, error and recovery without discarding local state',async()=>{
    const repository=new MockOwnerRepository();
    await repository.renameWorkspace('Sample portfolio');
    expect((await repository.getDashboard('empty')).properties).toHaveLength(0);
    expect((await repository.getDashboard('empty')).balances.every(b=>b.amountMinor===0)).toBe(true);
    await expect(repository.getDashboard('error')).rejects.toThrow('Simulated');
    expect((await repository.getDashboard('populated')).workspaceName).toBe('Sample portfolio');
    await expect(repository.setTaskDone('missing',true)).rejects.toThrow('not found');
    await expect(repository.renameWorkspace(' ')).rejects.toThrow('2–60');
  });
});
