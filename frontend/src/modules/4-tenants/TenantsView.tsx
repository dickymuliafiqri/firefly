import { useState, useCallback } from 'react';
import { useTenants, useStoreActions, useAppStore, buildSettingsPayload } from '@/core/state/store';
import type { TenantDTO } from '@/services/schema';
import { TenantTable } from './TenantTable';
import { KeyGeneratorModal } from './KeyGeneratorModal';
import { Button } from '@/components/ui/Button';
import { KeyRound } from 'lucide-react';
import { useSaveSettingsMutation } from '@/services/api';

export default function TenantsView() {
  const tenants = useTenants();
  const { addOrUpdateTenant, removeTenant, addToast } = useStoreActions();
  const saveMutation = useSaveSettingsMutation();

  const [modalOpen, setModalOpen] = useState(false);

  const handleToggleStatus = useCallback(
    (tenant: TenantDTO) => {
      const nextStatus = tenant.status === 'suspended' ? 'active' : 'suspended';
      const updated: TenantDTO = { ...tenant, status: nextStatus };
      addOrUpdateTenant(updated);

      saveMutation.mutate(buildSettingsPayload());

      addToast({
        title: `Tenant ${nextStatus === 'active' ? 'Activated' : 'Suspended'}`,
        message: `${tenant.name} is now ${nextStatus}.`,
        type: nextStatus === 'active' ? 'success' : 'warning',
      });
    },
    [addOrUpdateTenant, saveMutation, addToast]
  );

  const handleDelete = useCallback(
    (tenant: TenantDTO) => {
      const identifier = tenant.key_hash || tenant.name;
      const nextTenants = useAppStore
        .getState()
        .tenants.filter((t) => (t.key_hash || t.name) !== identifier);
      removeTenant(identifier);

      saveMutation.mutate(
        buildSettingsPayload({ tenants: nextTenants, manage_tenants: true })
      );

      addToast({
        title: 'Tenant Deleted',
        message: `${tenant.name} has been removed.`,
        type: 'info',
      });
    },
    [removeTenant, saveMutation, addToast]
  );

  return (
    <div className="flex flex-col gap-6 w-full animate-in fade-in duration-300">
      {/* Top Action Bar */}
      <div className="flex items-center justify-end">
        <Button
          variant="minimal"
          size="sm"
          onClick={() => setModalOpen(true)}
          leftIcon={<KeyRound className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />}
        >
          Issue Tenant Key
        </Button>
      </div>

      {/* Tenant Table Card */}
      <div className="rounded-xl border border-white/[0.06] bg-transparent overflow-hidden">
        <TenantTable
          tenants={tenants}
          onToggleStatus={handleToggleStatus}
          onDelete={handleDelete}
        />
      </div>

      {/* Key Generator Modal */}
      <KeyGeneratorModal
        isOpen={modalOpen}
        onClose={() => setModalOpen(false)}
      />
    </div>
  );
}
