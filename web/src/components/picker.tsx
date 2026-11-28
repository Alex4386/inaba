import type { ReactNode } from "react";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

/** A dropdown for choosing one of a few named options. */
export function Picker<T extends string>({
  value,
  onChange,
  options,
  label,
  size,
  className,
  disabled,
}: {
  disabled?: boolean;
  value: T;
  onChange: (value: T) => void;
  /** An icon, where given, goes before its label, in the list and on the choice made. */
  options: { value: T; label: string; icon?: ReactNode }[];
  /** What is being chosen, for screen readers. */
  label: string;
  size?: "sm" | "default";
  className?: string;
}) {
  return (
    <Select items={options} value={value} disabled={disabled} onValueChange={(next) => next && onChange(next as T)}>
      <SelectTrigger size={size} className={className} aria-label={label}>
        <SelectValue>
          {(chosen: T) => {
            const option = options.find((o) => o.value === chosen);
            return option && (
              <>
                {option.icon}
                {option.label}
              </>
            );
          }}
        </SelectValue>
      </SelectTrigger>
      <SelectContent>
        {options.map((option) => (
          <SelectItem key={option.value} value={option.value}>
            {option.icon}
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
