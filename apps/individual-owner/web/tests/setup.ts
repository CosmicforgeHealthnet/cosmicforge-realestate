import '@testing-library/jest-dom/vitest';
import { afterEach, vi } from 'vitest';
import { cleanup } from '@testing-library/react';
afterEach(()=>cleanup());
const storage = new Map<string,string>();
const localStorageMock = {getItem:(key:string)=>storage.get(key) ?? null,setItem:(key:string,value:string)=>{storage.set(key,value)},removeItem:(key:string)=>{storage.delete(key)},clear:()=>storage.clear(),key:(index:number)=>[...storage.keys()][index] ?? null,get length(){return storage.size}};
vi.stubGlobal('localStorage',localStorageMock);
Object.defineProperty(window,'matchMedia',{writable:true,value:vi.fn(()=>({matches:false,addEventListener:vi.fn(),removeEventListener:vi.fn()}))});
