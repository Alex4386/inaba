import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

/** A dropdown for choosing one of a few named options. */
export function Picker<T extends string>({
  value,
  onChange,
  options,
  label,
  size,
  className,
}: {
  value: T;
  onChange: (value: T) => void;
  options: { value: T; label: string }[];
  /** What is being chosen, for screen readers. */
  label: string;
  size?: "sm" | "default";
  className?: string;
}) {
  return (
    <Select items={options} value={value} onValueChange={(next) => next && onChange(next as T)}>
      <SelectTrigger size={size} className={className} aria-label={label}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {options.map((option) => (
          <SelectItem key={option.value} value={option.value}>
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
