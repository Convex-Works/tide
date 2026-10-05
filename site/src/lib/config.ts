// Every environment variable the tide server reads, grouped as the
// configuration page shows them. scripts/check-config-reference.mjs fails the
// site's check when this list and server/internal/config/config.go disagree.

export type EnvVar = {
  name: `TIDE_${string}`;
  /** The value used when the variable is unset. */
  default: string;
  /** Required outside dev mode: tide refuses to start without it. */
  required?: boolean;
  /** The shortest value tide accepts outside dev mode. */
  minLength?: number;
  description: string;
};

export type EnvGroup = { title: string; vars: EnvVar[] };

export const envGroups: EnvGroup[] = [
  {
    title: 'Server',
    vars: [
      { name: 'TIDE_ADDR', default: ':8080', description: 'Listen address.' },
      {
        name: 'TIDE_BASE_URL',
        default: 'http://localhost:8080',
        description:
          'The HTTPS origin people open, scheme and port included. Used for redirects, the sign-in callback and cookies.'
      },
      {
        name: 'TIDE_DB_PATH',
        default: './data/tide.db',
        description:
          'SQLite database. The image sets `/data/tide.db`; mount a durable volume there.'
      },
      {
        name: 'TIDE_SESSION_SECRET',
        default: '',
        required: true,
        minLength: 32,
        description: 'Signs session cookies. Random data.'
      },
      {
        name: 'TIDE_DEV_MODE',
        default: 'false',
        description:
          'Accepts the development secrets in the repository and an unauthenticated token route. Never in production.'
      }
    ]
  },
  {
    title: 'Media server',
    vars: [
      {
        name: 'TIDE_LIVEKIT_URL',
        default: 'ws://localhost:7880',
        description: 'The media server as tide reaches it, inside the cluster.'
      },
      {
        name: 'TIDE_LIVEKIT_PUBLIC_URL',
        default: 'ws://localhost:7880',
        description: 'The media server as browsers reach it: `wss://MEDIA_HOST`.'
      },
      {
        name: 'TIDE_LIVEKIT_API_KEY',
        default: '',
        required: true,
        description:
          'Key ID shared by tide, the media server and the recorder. Letters, digits, `-` and `_`.'
      },
      {
        name: 'TIDE_LIVEKIT_API_SECRET',
        default: '',
        required: true,
        minLength: 32,
        description: 'The secret for that key.'
      }
    ]
  },
  {
    title: 'Sign-in',
    vars: [
      {
        name: 'TIDE_OIDC_ISSUER',
        default: 'http://localhost:5556/dex',
        description: 'Issuer URL, exactly as the issuer’s discovery document states it.'
      },
      { name: 'TIDE_OIDC_CLIENT_ID', default: 'tide', description: 'Registered client ID.' },
      {
        name: 'TIDE_OIDC_CLIENT_SECRET',
        default: '',
        required: true,
        minLength: 16,
        description: 'Registered client secret.'
      },
      {
        name: 'TIDE_USER_GROUPS',
        default: '',
        description:
          'Comma-separated groups allowed to sign in, matched exactly. Empty allows every user of the issuer.'
      },
      {
        name: 'TIDE_ADMIN_GROUPS',
        default: '',
        description:
          'Comma-separated groups whose members manage every room. Always allowed to sign in.'
      }
    ]
  },
  {
    title: 'Storage',
    vars: [
      {
        name: 'TIDE_S3_ENDPOINT',
        default: 'http://localhost:9000',
        description: 'The store as tide reaches it.'
      },
      {
        name: 'TIDE_S3_PUBLIC_ENDPOINT',
        default: 'http://localhost:9000',
        description: 'The store as browsers reach it. Public HTTPS; download links name this host.'
      },
      {
        name: 'TIDE_S3_EGRESS_ENDPOINT',
        default: 'http://minio:9000',
        description: 'The store as the recorder reaches it.'
      },
      {
        name: 'TIDE_S3_BUCKET',
        default: 'tide-recordings',
        description: 'An existing bucket. Recordings go under `recordings/`.'
      },
      { name: 'TIDE_S3_REGION', default: 'us-east-1', description: 'Signing region.' },
      { name: 'TIDE_S3_ACCESS_KEY', default: '', required: true, description: 'Access key.' },
      {
        name: 'TIDE_S3_SECRET_KEY',
        default: '',
        required: true,
        minLength: 16,
        description: 'Secret key.'
      }
    ]
  },
  {
    title: 'Recording',
    vars: [
      {
        name: 'TIDE_EGRESS_TEMPLATE_URL',
        default: 'TIDE_BASE_URL + /egress-template',
        description:
          'The page the recorder loads to draw a meeting. In Kubernetes: `http://tide:8080/egress-template`.'
      }
    ]
  },
  {
    title: 'Rate limits',
    vars: [
      {
        name: 'TIDE_TRUSTED_PROXIES',
        default: '',
        description:
          'Comma-separated IPs or CIDRs whose `X-Forwarded-For` tide believes. Only your ingress. An invalid entry stops startup.'
      },
      {
        name: 'TIDE_JOIN_RATE_LIMIT',
        default: '10',
        description: 'Joins per client per minute. Also bounds room lookups.'
      },
      {
        name: 'TIDE_WAIT_RATE_LIMIT',
        default: '20',
        description: 'Lobby wait streams per client per minute.'
      },
      {
        name: 'TIDE_LOGIN_RATE_LIMIT',
        default: '10',
        description: 'Sign-in redirects per client per minute.'
      },
      {
        name: 'TIDE_PAIR_RATE_LIMIT',
        default: '10',
        description: 'Machine pairings per client per minute, with transcripts on.'
      }
    ]
  },
  {
    title: 'Transcripts',
    vars: [
      {
        name: 'TIDE_TRANSCRIPTS',
        default: 'false',
        description: 'Turns on [transcripts](/docs/transcripts) and machine pairing.'
      }
    ]
  }
];

export const envVars: EnvVar[] = envGroups.flatMap((group) => group.vars);

/** The configuration reference as Markdown, the same text the site renders. */
export function configurationMarkdown(): string {
  const cell = (text: string) => text.replace(/\|/g, '\\|');
  const sections = envGroups.map((group) => {
    const rows = group.vars.map((v) => {
      const rule = v.required
        ? `**Required**${v.minLength ? `, ${v.minLength}+ characters` : ''}. `
        : '';
      const fallback = v.required ? '—' : v.default ? `\`${v.default}\`` : 'empty';
      return `| \`${v.name}\` | ${fallback} | ${cell(rule + v.description)} |`;
    });
    return [
      `## ${group.title}`,
      '',
      '| Variable | Default | Meaning |',
      '| --- | --- | --- |',
      ...rows
    ].join('\n');
  });
  return [
    '# Configuration',
    '',
    'tide reads only environment variables. Outside development it refuses to start while a required variable is missing, too short, or a value from the repository’s development setup.',
    '',
    ...sections.flatMap((section) => [section, ''])
  ].join('\n');
}
