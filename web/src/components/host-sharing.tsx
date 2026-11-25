import { Trash2Icon } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { hosts as hostsApi } from "@/api";
import { HostInvites } from "@/components/host-invites";
import { UserAvatar } from "@/components/presence";
import { Card } from "@/components/section-dialog";
import { Button } from "@/components/ui/button";
import type { Host, ListHostMembersResponse } from "@/gen/inaba/v1/server_pb";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

/** Who may use a host besides everyone it is open to: its invite links, and who came in by them. */
export function HostSharing({ host }: { host: Host }) {
  const { t } = useI18n();
  const [state, setState] = useState<ListHostMembersResponse>();
  const load = useCallback(() => hostsApi.listHostMembers({ hostId: host.id }).then(setState, showError), [host.id]);
  useEffect(() => {
    void load();
  }, [load]);
  return (
    <div className="grid gap-4">
      <p className="text-muted-foreground text-sm">{t(host.shared ? "sharing.descriptionShared" : "sharing.description")}</p>
      <HostInvites host={host} />
      <div className="grid gap-2">
        <h3 className="text-sm font-medium">{t("sharing.members")}</h3>
        {state && state.members.length === 0 ? (
          <p className="text-muted-foreground text-sm">{t("sharing.noMembers")}</p>
        ) : (
          <Card>
            {state?.members.map((m) => (
              <div key={m.userId} className="flex items-center gap-3 px-4 py-3">
                <UserAvatar id={m.userId} name={m.name} url={m.avatarUrl} className="size-7" />
                <span className="min-w-0 flex-1 truncate text-sm font-medium">{m.name}</span>
                <Button size="icon-sm" variant="ghost" aria-label={t("sharing.remove", { name: m.name })} title={t("sharing.remove", { name: m.name })} onClick={() => hostsApi.removeHostMember({ hostId: host.id, userId: m.userId }).then(load, showError)}>
                  <Trash2Icon />
                </Button>
              </div>
            ))}
          </Card>
        )}
      </div>
    </div>
  );
}
