import React from 'react';
import type { TabId } from '@/core/layout/Header';

export interface FireflyModuleDefinition {
  id: TabId;
  title: string;
  description: string;
  order: number;
  icon: React.ComponentType<{ className?: string }>;
  component: React.LazyExoticComponent<React.ComponentType<any>>;
  preload: () => Promise<unknown>;
}
