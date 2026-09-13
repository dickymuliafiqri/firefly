import React from 'react';

/**
 * Shimmer Loading Skeleton for Lazy Loaded Modules
 * Follows Vercel React Best Practices: async-suspense-boundaries & rendering-hoist-jsx
 */
export const ModuleSkeleton = React.memo(function ModuleSkeleton() {
  return (
    <div className="w-full flex flex-col gap-6 animate-pulse">
      {/* Header bar skeleton */}
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <div className="h-5 w-48 rounded bg-white/[0.04]" />
          <div className="h-3 w-72 rounded bg-white/[0.02]" />
        </div>
        <div className="h-8 w-24 rounded border border-white/[0.06] bg-transparent" />
      </div>

      {/* Content area skeleton */}
      <div className="h-80 w-full rounded-xl bg-transparent border border-white/[0.06]" />

      {/* Grid cards skeleton */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <div className="h-28 rounded-xl bg-transparent border border-white/[0.06]" />
        <div className="h-28 rounded-xl bg-transparent border border-white/[0.06]" />
        <div className="h-28 rounded-xl bg-transparent border border-white/[0.06]" />
      </div>
    </div>
  );
});
