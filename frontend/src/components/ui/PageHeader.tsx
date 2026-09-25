import type { ReactNode } from 'react';
import { Button } from '@/components/ui/Button';

export interface PageHeaderProps {
  title: string;
  description?: string;
  actions?: ReactNode;
}

export function PageHeader({ title, description, actions }: PageHeaderProps) {
  return (
    <div className="page-header">
      <div>
        <h1>{title}</h1>
        {description ? <p>{description}</p> : null}
      </div>
      {actions ? <div className="actions">{actions}</div> : null}
    </div>
  );
}

export interface KpiCardProps {
  label: string;
  value: string;
  unit?: string;
  tone?: 'plain' | 'ok' | 'warn' | 'danger';
}

export function KpiCard({ label, value, unit, tone = 'plain' }: KpiCardProps) {
  return (
    <div className="kpi">
      <span className="label">{label}</span>
      <div className={tone === 'plain' ? 'value' : `value ${tone}`}>
        {value}
        {unit ? <span className="unit">{unit}</span> : null}
      </div>
    </div>
  );
}

export function KpiGrid({ children }: { children: ReactNode }) {
  return <div className="kpi-grid">{children}</div>;
}

export { Button };
