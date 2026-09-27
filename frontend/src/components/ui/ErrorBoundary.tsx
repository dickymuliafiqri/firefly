import { Component, type ErrorInfo, type ReactNode } from 'react';

interface ErrorBoundaryProps {
  children: ReactNode;
  /** Human label of the wrapped subtree, e.g. the page title. */
  title?: string;
}

interface ErrorBoundaryState {
  error: Error | null;
}

/**
 * Catches render-time exceptions inside its subtree and renders a recovery
 * card instead of letting React unmount the whole application. Without a
 * boundary anywhere in the tree, a single throwing component (e.g. a page
 * reading a field the backend serialized as null) used to leave the dashboard
 * as a bare background — no sidebar, no navbar, no way to navigate out.
 *
 * `key` the boundary with the current page id so navigating away remounts it
 * and clears the error state.
 */
export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('[ErrorBoundary]', this.props.title ?? 'subtree', error, info.componentStack);
  }

  private reset = () => {
    this.setState({ error: null });
  };

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;

    return (
      <div className="page-col">
        <div className="card" style={{ borderLeft: '3px solid var(--danger)' }}>
          <div className="card-body">
            <div style={{ fontSize: 13, fontWeight: 600 }}>
              {this.props.title ? `“${this.props.title}” failed to render` : 'Something went wrong'}
            </div>
            <div className="hint" style={{ marginTop: 4, fontSize: 12, color: 'var(--muted)' }}>
              {error.message || 'Unknown error'}
            </div>
            <div className="hint" style={{ marginTop: 4, fontSize: 12, color: 'var(--faint)' }}>
              The rest of the dashboard is still usable. Try again, or reload the page.
            </div>
            <div style={{ marginTop: 12, display: 'flex', gap: 8 }}>
              <button type="button" className="btn btn-secondary" onClick={this.reset}>
                Try again
              </button>
              <button type="button" className="btn btn-primary" onClick={() => window.location.reload()}>
                Reload page
              </button>
            </div>
          </div>
        </div>
      </div>
    );
  }
}