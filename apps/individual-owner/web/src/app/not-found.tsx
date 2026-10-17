import Link from 'next/link';
export default function NotFound() { return <div className="state"><h1>Page not found</h1><p>This route is not part of the workspace.</p><Link href="/">Return to dashboard</Link></div>; }
