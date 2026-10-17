'use client';
export default function GlobalError({reset}:{reset:()=>void}) {
  return <html lang="en"><body><main style={{padding:32,fontFamily:'system-ui'}}><h1>Workspace unavailable</h1><p>The local demonstration encountered an application error.</p><button onClick={reset}>Reload workspace</button></main></body></html>;
}
