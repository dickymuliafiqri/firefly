import type { ReactNode } from 'react';

export interface SegmentedItem {
  id: string;
  label: string;
}

export interface SegmentedProps {
  items: SegmentedItem[];
  value: string;
  onChange: (id: string) => void;
  ariaLabel?: string;
}

/** Segmented control — active item has raised background + ink text (DESIGN_RULES §4). */
export function Segmented({ items, value, onChange, ariaLabel }: SegmentedProps) {
  return (
    <div className="segmented" role="tablist" aria-label={ariaLabel}>
      {items.map((it) => (
        <button
          key={it.id}
          role="tab"
          aria-selected={it.id === value}
          className={it.id === value ? 'active' : undefined}
          onClick={() => onChange(it.id)}
        >
          {it.label}
        </button>
      ))}
    </div>
  );
}

export interface FieldProps {
  label: string;
  htmlFor?: string;
  hint?: string;
  children: ReactNode;
}

export function Field({ label, htmlFor, hint, children }: FieldProps) {
  return (
    <div className="field">
      <label htmlFor={htmlFor}>{label}</label>
      {children}
      {hint ? <span className="hint">{hint}</span> : null}
    </div>
  );
}

export interface SwitchRowProps {
  title: string;
  description?: string;
  checked: boolean;
  onChange: (v: boolean) => void;
  ariaLabel: string;
}

export function SwitchRow({ title, description, checked, onChange, ariaLabel }: SwitchRowProps) {
  return (
    <div className="switch-row">
      <div className="info">
        <h3>{title}</h3>
        {description ? <p>{description}</p> : null}
      </div>
      <span className="switch">
        <input
          type="checkbox"
          checked={checked}
          aria-label={ariaLabel}
          onChange={(e) => onChange(e.target.checked)}
        />
        <span className="track" />
      </span>
    </div>
  );
}
