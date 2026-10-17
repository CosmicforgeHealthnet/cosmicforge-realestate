'use client';
import {Suspense} from 'react';
import {useParams} from 'next/navigation';
import {WorkspaceScreen} from '@/features/workspace/workspace-screen';
import {LoadingState} from '@/components/states';
export default function PropertyPage(){const {id}=useParams<{id:string}>();return <Suspense fallback={<LoadingState/>}><WorkspaceScreen key={id} slug="properties" segments={['property',id]}/></Suspense>}
