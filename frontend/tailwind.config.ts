import type { Config } from 'tailwindcss';

const config: Config = {
  content: ['./index.html', './src/**/*.{js,ts,jsx,tsx}'],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        'night-top': '#020617',
        'night-bottom': '#08152a',
        surface: 'rgba(15, 23, 42, 0.65)',
        card: 'rgba(30, 41, 59, 0.45)',
        overlay: 'rgba(2, 6, 23, 0.85)',
        biolum: {
          glow: '#f0ffb4',
          aura: '#bef264',
          dim: 'rgba(190, 242, 100, 0.15)',
        },
        accent: {
          emerald: '#10b981',
          cyan: '#06b6d4',
          amber: '#f59e0b',
          rose: '#f43f5e',
          violet: '#8b5cf6',
        },
        border: {
          subtle: 'rgba(255, 255, 255, 0.06)',
          medium: 'rgba(255, 255, 255, 0.12)',
          focus: 'rgba(190, 242, 100, 0.40)',
        },
      },
      fontFamily: {
        sans: ['Inter', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        mono: ['JetBrains Mono', 'ui-monospace', 'SFMono-Regular', 'monospace'],
      },
      animation: {
        'aurora': 'aurora 20s ease infinite alternate',
        'pulse-slow': 'pulse 4s cubic-bezier(0.4, 0, 0.6, 1) infinite',
        'glow': 'glow 3s ease-in-out infinite alternate',
      },
      keyframes: {
        aurora: {
          '0%': { opacity: '0.015', transform: 'scale(1) translateY(0)' },
          '50%': { opacity: '0.03', transform: 'scale(1.05) translateY(-10px)' },
          '100%': { opacity: '0.02', transform: 'scale(1) translateY(0)' },
        },
        glow: {
          '0%': { filter: 'drop-shadow(0 0 4px rgba(190, 242, 100, 0.3))' },
          '100%': { filter: 'drop-shadow(0 0 12px rgba(190, 242, 100, 0.6))' },
        },
      },
      boxShadow: {
        glass: '0 8px 32px 0 rgba(0, 0, 0, 0.37)',
        'glass-inset': 'inset 0 1px 0 0 rgba(255, 255, 255, 0.08)',
        'biolum-sm': '0 0 10px rgba(190, 242, 100, 0.3)',
        'biolum-md': '0 0 20px rgba(190, 242, 100, 0.4)',
      },
    },
  },
  plugins: [],
};

export default config;
