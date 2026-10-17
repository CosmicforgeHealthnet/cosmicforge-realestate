import { CircleAlert, LoaderCircle } from 'lucide-react';
export function LoadingState() { return <div className="state" role="status"><LoaderCircle aria-hidden="true"/> Loading demo records…</div>; }
export function ErrorState({message,retry}:{message:string;retry?:()=>void}) { return <div className="state" role="alert"><CircleAlert aria-hidden="true"/><h2>Unable to show demo records</h2><p>{message}</p>{retry ? <button onClick={retry}>Try again</button> : null}</div>; }
export function EmptyState({title,children}:{title:string;children:React.ReactNode}) { return <div className="state"><h2>{title}</h2><p>{children}</p></div>; }
