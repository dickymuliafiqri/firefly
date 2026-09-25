import React from 'react';

export interface FireflyModuleDefinition {
  title: string;
  description: string;
  order: number;
  icon: React.ComponentType<{ className?: string }>;
  component: React.LazyExoticComponent<React.ComponentType<any>>;
  preload: () => Promise<unknown>;
}
