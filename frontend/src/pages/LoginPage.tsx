import { useState, type FormEvent } from 'react';
import { Eye, EyeOff, Lock, ArrowRight, ShieldCheck } from 'lucide-react';
import { loginApi } from '@/services/api';
import { useAuthStore } from '@/state/auth';
import { navigate } from '@/lib/router';

export function LoginPage() {
  const [password, setPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const setToken = useAuthStore((s) => s.setToken);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (!password.trim() || loading) return;

    setError(null);
    setLoading(true);

    try {
      const res = await loginApi(password.trim());
      setToken(res.token);

      // Check redirect parameter in URL if present
      const hash = window.location.hash;
      const redirectMatch = hash.match(/[?&]redirect=([a-zA-Z0-9_-]+)/);
      const target = redirectMatch ? redirectMatch[1] : 'overview';
      navigate(target);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Incorrect password.');
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="login-screen">
      <div className="login-card card">
        <div className="login-card-header">
          <div className="login-emblem">
            <Lock style={{ width: 20, height: 20 }} />
          </div>
          <h1 className="login-wordmark">Firefly</h1>
          <p className="login-sub">Enter master password to manage gateway.</p>
        </div>

        <div className="login-card-body">
          <form onSubmit={handleSubmit}>
            <div className="field">
              <label htmlFor="login-password">Password</label>
              <div className="login-row">
                <div className="login-input-wrap">
                  <input
                    id="login-password"
                    type={showPassword ? 'text' : 'password'}
                    value={password}
                    placeholder="••••••••"
                    autoFocus
                    required
                    disabled={loading}
                    onChange={(e) => {
                      setPassword(e.target.value);
                      if (error) setError(null);
                    }}
                  />
                  <button
                    type="button"
                    className="login-eye"
                    aria-label={showPassword ? 'Hide password' : 'Show password'}
                    onClick={() => setShowPassword(!showPassword)}
                  >
                    {showPassword ? <EyeOff style={{ width: 16, height: 16 }} /> : <Eye style={{ width: 16, height: 16 }} />}
                  </button>
                </div>
                <button type="submit" className="btn btn-primary" disabled={loading || !password.trim()}>
                  {loading ? 'Verifying…' : 'Unlock'}
                  {!loading && <ArrowRight style={{ width: 14, height: 14 }} />}
                </button>
              </div>
            </div>

            {error && <p className="login-error">{error}</p>}
          </form>
        </div>

        <div className="login-card-foot">
          <ShieldCheck style={{ width: 14, height: 14 }} />
          <span>Session saved in this browser only.</span>
        </div>
      </div>
    </div>
  );
}
