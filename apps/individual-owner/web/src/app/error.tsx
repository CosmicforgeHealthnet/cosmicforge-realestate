'use client';
import { ErrorState } from '@/components/states';
export default function ErrorPage({reset}:{reset:()=>void}) { return <ErrorState message="The workspace encountered a local error. Retry to reload this view." retry={reset}/>; }
