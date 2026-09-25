import { Server } from 'lucide-react';

export function OpenAIIcon({ size = 20, className = '' }: { size?: number; className?: string }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="currentColor" className={className} aria-hidden="true">
      <path d="M22.2819 9.8211a5.9847 5.9847 0 0 0-.5157-4.9108 6.0462 6.0462 0 0 0-6.5098-2.9A6.0651 6.0651 0 0 0 4.9807 4.1818a5.9847 5.9847 0 0 0-3.9977 2.9 6.0462 6.0462 0 0 0 .7427 7.0966 5.98 5.98 0 0 0 .511 4.9107 6.051 6.051 0 0 0 6.5146 2.9001A5.9847 5.9847 0 0 0 13.2599 24a6.0557 6.0557 0 0 0 5.7718-4.2058 5.9894 5.9894 0 0 0 3.9977-2.9001 6.0557 6.0557 0 0 0-.7475-7.0729zm-9.022 12.6081a4.4755 4.4755 0 0 1-2.8764-1.0408l.1419-.0804 4.7783-2.7582a.7948.7948 0 0 0 .3927-.6813v-6.7369l2.02 1.1683a.071.071 0 0 1 .038.052v5.5826a4.504 4.504 0 0 1-4.4945 4.4947zm-9.66-4.9954a4.4708 4.4708 0 0 1-.5346-3.0137l.142.0852 4.783 2.7582a.7712.7712 0 0 0 .7806 0l5.8428-3.3685v2.3324a.0804.0804 0 0 1-.0332.0615L9.74 19.9502a4.4992 4.4992 0 0 1-6.1401-2.5164zM2.3428 7.897a4.485 4.485 0 0 1 2.3655-1.9728V11.6a.7664.7664 0 0 0 .3879.6765l5.8144 3.3543-2.0201 1.1683a.0757.0757 0 0 1-.071 0l-4.8303-2.7866A4.504 4.504 0 0 1 2.3428 7.897zm16.5986 3.8558L13.1038 8.3843l2.0153-1.1635a.0804.0804 0 0 1 .071 0l4.8303 2.7913a4.4944 4.4944 0 0 1-.6765 8.1042v-5.6772a.79.79 0 0 0-.4027-.6863zm2.0107-3.0231l-.142-.0852-4.7735-2.7818a.7759.7759 0 0 0-.7854 0L9.4084 9.2312V6.8988a.0662.0662 0 0 1 .0284-.0615l4.8303-2.7866a4.4992 4.4992 0 0 1 6.6802 4.66zM8.3065 12.863l-2.02-1.1635a.0804.0804 0 0 1-.038-.0567V6.0742a4.4992 4.4992 0 0 1 7.3757-3.4537l-.142.0805L8.704 5.459a.7948.7948 0 0 0-.3927.6813zm1.0976-2.3654l2.602-1.4998 2.6069 1.4998v2.9994l-2.6069 1.4997-2.602-1.4997z" />
    </svg>
  );
}

export function GoogleIcon({ size = 20, className = '' }: { size?: number; className?: string }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" className={className} aria-hidden="true">
      <path fill="#4285F4" d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92c-.26 1.37-1.04 2.53-2.21 3.31v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.09z" />
      <path fill="#34A853" d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z" />
      <path fill="#FBBC05" d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.06H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.94l2.85-2.22.81-.63z" />
      <path fill="#EA4335" d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.06l3.66 2.84c.87-2.6 3.3-4.52 6.16-4.52z" />
    </svg>
  );
}

export function AnthropicIcon({ size = 20, className = '' }: { size?: number; className?: string }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="currentColor" className={className} aria-hidden="true">
      <path d="M13.827 3.52h3.603l6.57 16.96h-3.627l-1.371-3.68H12.08l-1.373 3.68H7.08zm-.747 10.4h4.56l-2.28-6.107zm-7.653 6.56l5.72-14.747H7.544L1.824 20.48z" />
    </svg>
  );
}

export function GrokIcon({ size = 20, className = '' }: { size?: number; className?: string }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="currentColor" className={className} aria-hidden="true">
      <path d="M18.244 2.25h3.308l-7.227 8.26 8.502 11.24H16.17l-5.214-6.817L4.99 21.75H1.68l7.73-8.835L1.254 2.25H8.08l4.713 6.231zm-1.161 17.52h1.833L7.084 4.126H5.117z" />
    </svg>
  );
}


export function ClineIcon({ size = 20, className = '' }: { size?: number; className?: string }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className={className} aria-hidden="true">
      <rect x="3" y="4" width="18" height="15" rx="3" />
      <circle cx="8.5" cy="11.5" r="1.5" fill="currentColor" />
      <circle cx="15.5" cy="11.5" r="1.5" fill="currentColor" />
      <path d="M10 15h4" />
      <path d="M12 4V2" />
    </svg>
  );
}

export function CodeBuddyIcon({ size = 20, className = '' }: { size?: number; className?: string }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className={className} aria-hidden="true">
      <polyline points="16 18 22 12 16 6" />
      <polyline points="8 6 2 12 8 18" />
      <line x1="12" y1="2" x2="12" y2="6" />
      <circle cx="12" cy="12" r="2" fill="currentColor" />
    </svg>
  );
}

export function OpenCodeIcon({ size = 20, className = '' }: { size?: number; className?: string }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className={className} aria-hidden="true">
      <polyline points="7 8 3 12 7 16" />
      <polyline points="17 8 21 12 17 16" />
      <line x1="14" y1="4" x2="10" y2="20" />
    </svg>
  );
}

export function QoderIcon({ size = 20, className = '' }: { size?: number; className?: string }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className={className} aria-hidden="true">
      <circle cx="11.5" cy="11.5" r="7.5" />
      <path d="M17 17l4 4" />
      <path d="M8.5 11.5h6" />
    </svg>
  );
}

export function DeepSeekIcon({ size = 20, className = '' }: { size?: number; className?: string }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="currentColor" className={className} aria-hidden="true">
      <path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 17.93c-3.95-.49-7-3.85-7-7.93 0-.62.08-1.21.21-1.79L9 15v1c0 1.1.9 2 2 2v1.93zm6.9-2.54c-.26-.81-1-1.39-1.9-1.39h-1v-3c0-.55-.45-1-1-1H8v-2h2c.55 0 1-.45 1-1V7h2c1.1 0 2-.9 2-2v-.41c2.93 1.19 5 4.06 5 7.41 0 2.08-.8 3.97-2.1 5.39z" />
    </svg>
  );
}

export function OllamaIcon({ size = 20, className = '' }: { size?: number; className?: string }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className={className} aria-hidden="true">
      <path d="M12 2a4 4 0 0 0-4 4v2H7a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-8a2 2 0 0 0-2-2h-1V6a4 4 0 0 0-4-4z" />
      <circle cx="9.5" cy="12.5" r="1" fill="currentColor" />
      <circle cx="14.5" cy="12.5" r="1" fill="currentColor" />
      <path d="M10 16h4" />
    </svg>
  );
}

export function AzureIcon({ size = 20, className = '' }: { size?: number; className?: string }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="currentColor" className={className} aria-hidden="true">
      <path d="M13.05 2.25 4.5 16.5h5.55l3-5.25 4.5 9.75H24L13.05 2.25zM3.45 18 0 24h9.75l3.45-6H3.45z" />
    </svg>
  );
}

export interface ProviderIconProps {
  protocol?: string;
  name?: string;
  size?: number;
  className?: string;
}

export function ProviderIcon({ protocol, name, size = 18, className = '' }: ProviderIconProps) {
  const p = (protocol || '').toLowerCase().trim();
  const n = (name || '').toLowerCase().trim();

  // 1. Antigravity / Google Cloud Code / Gemini
  if (
    p === 'antigravity' ||
    p === 'antigravity-go' ||
    p === 'google' ||
    p === 'gemini' ||
    n.includes('google') ||
    n.includes('gemini') ||
    n.includes('antigravity')
  ) {
    return <GoogleIcon size={size} className={className} />;
  }

  // 2. OpenAI
  if (p === 'openai' || n.includes('openai')) {
    return <OpenAIIcon size={size} className={`text-[#10a37f] ${className}`} />;
  }

  // 3. Anthropic / Claude
  if (p === 'anthropic' || n.includes('anthropic') || n.includes('claude')) {
    return <AnthropicIcon size={size} className={`text-[#d97757] ${className}`} />;
  }

  // 4. Grok / xAI
  if (p === 'grok-cli' || p === 'grok' || n.includes('grok') || n.includes('xai')) {
    return <GrokIcon size={size} className={`text-ink ${className}`} />;
  }

  // 5. Cline
  if (p === 'cline' || n.includes('cline')) {
    return <ClineIcon size={size} className={`text-[#6366f1] ${className}`} />;
  }

  // 6. CodeBuddy
  if (p.startsWith('codebuddy') || n.includes('codebuddy')) {
    return <CodeBuddyIcon size={size} className={`text-[#3b82f6] ${className}`} />;
  }

  // 7. OpenCode
  if (p.startsWith('opencode') || n.includes('opencode')) {
    return <OpenCodeIcon size={size} className={`text-[#06b6d4] ${className}`} />;
  }

  // 8. Qoder
  if (p === 'qoder' || n.includes('qoder')) {
    return <QoderIcon size={size} className={`text-[#a855f7] ${className}`} />;
  }

  // 9. DeepSeek
  if (n.includes('deepseek')) {
    return <DeepSeekIcon size={size} className={`text-[#0ea5e9] ${className}`} />;
  }

  // 10. Ollama
  if (n.includes('ollama')) {
    return <OllamaIcon size={size} className={`text-[#f59e0b] ${className}`} />;
  }

  // 11. Azure
  if (n.includes('azure')) {
    return <AzureIcon size={size} className={`text-[#0089d6] ${className}`} />;
  }

  // Fallback generic server icon
  return <Server width={size} height={size} className={`text-muted ${className}`} />;
}
