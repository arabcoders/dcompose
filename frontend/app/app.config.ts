import { defineAppConfig } from '#imports'

export default defineAppConfig({
  ui: {
    primary: 'indigo',
    colors: {
      primary: 'indigo',
      secondary: 'amber',
      success: 'emerald',
      neutral: 'stone',
    },
    card: {
      variants: {
        variant: {
          outline: {
            root: 'app-card bg-elevated/20 ring-0',
          },
        },
      },
    },
    formField: {
      slots: {
        label: 'block font-bold text-default',
      },
    },
    tooltip: {
      slots: {
        content: 'pointer-events-auto',
      },
    },
    button: {
      defaultVariants: {
        size: 'sm',
      },
    },
  },
})
