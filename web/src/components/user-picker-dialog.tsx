import { ShieldIcon } from "lucide-react";
import { UserAvatar } from "@/components/presence";
import { Command, CommandDialog, CommandEmpty, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import type { User } from "@/gen/inaba/v1/server_pb";
import { useI18n } from "@/lib/i18n";

/**
 * Finds an account to act on by any of its names, as a search rather than a list to scroll: an
 * administrator with hundreds of accounts types a few letters. Stays open, so several are picked in a row;
 * those picked drop out of users as the caller updates it.
 */
export function UserPickerDialog({
  open,
  onOpenChange,
  users,
  title,
  onPick,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  users: User[];
  title: string;
  onPick: (user: User) => unknown;
}) {
  const { t } = useI18n();
  return (
    <CommandDialog open={open} onOpenChange={onOpenChange} title={title} description={t("userPicker.help")}>
      <Command>
        <CommandInput placeholder={t("userPicker.search")} />
        <CommandList>
          <CommandEmpty>{t("userPicker.none")}</CommandEmpty>
          {users.map((u) => (
            // Matched on every name it goes by; the id keeps two alike apart.
            <CommandItem key={u.id} value={u.id} keywords={[u.name, u.displayName].filter(Boolean)} onSelect={() => void onPick(u)}>
              <UserAvatar id={u.id} name={u.displayName || u.name} url={u.avatarUrl} size="sm" />
              <span className="min-w-0 truncate">{u.displayName || u.name}</span>
              {u.displayName && <span className="text-muted-foreground truncate text-xs">@{u.name}</span>}
              {u.admin && <ShieldIcon className="text-muted-foreground ml-auto size-3.5" aria-label={t("userPicker.admin")} />}
            </CommandItem>
          ))}
        </CommandList>
      </Command>
    </CommandDialog>
  );
}
