import { useEffect, useState } from 'react';
import { getStatus } from '@/api/status';

export default function Footer() {
  const [version, setVersion] = useState('');

  useEffect(() => {
    getStatus().then(s => setVersion(s.version || '')).catch(() => {});
  }, []);

  const year = new Date().getFullYear();

  return (
    <footer className="mt-8 py-4 text-center text-xs text-gray-400 dark:text-gray-500 select-none">
      <span>© {year} CC-Connect</span>
      {version && <span className="mx-1.5">·</span>}
      {version && <span>v{version}</span>}
      <span className="mx-1.5">·</span>
      <a
        href="https://github.com/chenhg5/cc-connect"
        target="_blank"
        rel="noopener noreferrer"
        className="hover:text-accent transition-colors"
      >
        GitHub
      </a>
    </footer>
  );
}
