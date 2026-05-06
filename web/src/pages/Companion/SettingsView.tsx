import { useState } from 'react';
import { cn } from '@/lib/utils';

export default function SettingsView() {
  const [server, setServer] = useState('https://cc-connect.local');
  const [polling, setPolling] = useState(1);
  const [pushEnabled, setPushEnabled] = useState(true);

  return (
    <div className="flex flex-col h-full bg-[#f6f1e7]">
      {/* Header */}
      <div className="flex-shrink-0 px-5 pt-4 pb-3">
        <h1 className="font-serif text-2xl font-medium text-[#1a1915] tracking-tight leading-none">
          Settings
        </h1>
        <p className="text-[11px] font-mono text-[#9e9590] mt-1 tracking-wider uppercase">
          v0.1
        </p>
      </div>

      {/* Settings groups */}
      <div className="flex-1 overflow-y-auto px-4 pb-6">
        <SettingsGroup label="Server">
          <SettingsRow label="Server URL">
            <input
              value={server}
              onChange={(e) => setServer(e.target.value)}
              className="flex-1 ml-3 bg-transparent text-right text-xs font-mono text-[#1a1915] outline-none border-0 min-w-0"
            />
          </SettingsRow>
          <SettingsRow label="Status">
            <div className="flex items-center gap-2">
              <span className="w-2 h-2 rounded-full bg-[#2d6a4f]" />
              <span className="text-xs font-mono text-[#6b6560]">Connected</span>
            </div>
          </SettingsRow>
          <button
            className={cn(
              'w-full text-left px-3.5 py-3 text-sm font-medium',
              'text-[#e85d3a]',
              'hover:bg-[#f0ebe1] transition-colors',
            )}
          >
            Test connection
          </button>
        </SettingsGroup>

        <SettingsGroup label="Behavior">
          <SettingsRow label="Polling interval">
            <div className="flex items-center gap-3">
              <span className="text-[10px] font-mono text-[#9e9590]">
                {polling.toFixed(1)}s
              </span>
              <input
                type="range"
                min="0.5"
                max="3"
                step="0.5"
                value={polling}
                onChange={(e) => setPolling(parseFloat(e.target.value))}
                className="w-24 accent-[#e85d3a]"
              />
            </div>
          </SettingsRow>
          <SettingsRow label="Push notifications">
            <Switch on={pushEnabled} onClick={() => setPushEnabled(!pushEnabled)} />
          </SettingsRow>
        </SettingsGroup>

        <SettingsGroup label="Account">
          <SettingsRow label="Version">
            <span className="text-[11px] font-mono text-[#9e9590]">
              0.1.0 · Aelios Companion
            </span>
          </SettingsRow>
          <SettingsRow label="Server">
            <span className="text-[11px] font-mono text-[#9e9590]">
              cc-connect
            </span>
          </SettingsRow>
        </SettingsGroup>

        <p className="text-center pt-5 font-serif text-xs text-[#b5b0a8] italic">
          Aelios Companion · v0.1 · 2026-05
        </p>
      </div>
    </div>
  );
}

function SettingsGroup({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="mt-4">
      <p className="text-[10px] font-mono text-[#9e9590] tracking-widest uppercase px-1 pb-2">
        {label}
      </p>
      <div
        className={cn(
          'rounded-xl overflow-hidden',
          'bg-[#fffcf5] border border-[#e5dfd5]',
        )}
      >
        {children}
      </div>
    </div>
  );
}

function SettingsRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between px-3.5 py-3 border-b border-[#e5dfd5] last:border-b-0 gap-3">
      <span className="text-sm text-[#6b6560] flex-shrink-0">{label}</span>
      {children}
    </div>
  );
}

function Switch({ on, onClick }: { on: boolean; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      className={cn(
        'relative w-[38px] h-[22px] rounded-full transition-colors duration-200 flex-shrink-0',
        on ? 'bg-[#1a1915]' : 'bg-[#d4cfc5]',
      )}
    >
      <div
        className={cn(
          'absolute top-[2px] w-[18px] h-[18px] rounded-full bg-[#fffcf5] transition-all duration-200',
          on ? 'left-[18px]' : 'left-[2px]',
        )}
      />
    </button>
  );
}
