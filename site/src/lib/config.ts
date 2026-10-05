// Every environment variable the tide server reads, grouped as the
// configuration page shows them. scripts/check-config-reference.mjs fails the
// site's check when this list and server/internal/config/config.go disagree.

export type EnvVar = {
  name: `TIDE_${string}`;
  /** The value used when the variable is unset, as a literal. */
  default: string;
  /** Said instead of a literal default, in Markdown, when it depends. */
  defaultText?: string;
  /** The mode that needs it, as in "with sign-in": outside dev mode, tide then refuses to start without it. */
  required?: string;
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
          'The HTTPS origin people open, scheme and port included. Used for redirects, the sign-in callback, cookies and the signaling address browsers get.'
      },
      {
        name: 'TIDE_SESSION_SECRET',
        default: '',
        defaultText: 'generated per start when anonymous',
        required: 'with sign-in',
        minLength: 32,
        description: 'Signs session cookies. Random data, checked whenever it is set.'
      },
      {
        name: 'TIDE_DB_PATH',
        default: './data/tide.db',
        description:
          'SQLite database, with sign-in. The image sets `/data/tide.db`; mount a durable volume there. Anonymous tide keeps rooms in memory.'
      },
      {
        name: 'TIDE_DEV_MODE',
        default: 'false',
        description:
          'Accepts the development secrets in the repository and an unauthenticated token route. Turns nothing else on. Never in production.'
      }
    ]
  },
  {
    title: 'Sign-in',
    vars: [
      {
        name: 'TIDE_OIDC_ISSUER',
        default: '',
        defaultText: 'none: anonymous',
        description:
          'Turns sign-in on. The issuer URL, exactly as its discovery document states it. Without it anyone can create a room, and rooms live in memory.'
      },
      { name: 'TIDE_OIDC_CLIENT_ID', default: 'tide', description: 'Registered client ID.' },
      {
        name: 'TIDE_OIDC_CLIENT_SECRET',
        default: '',
        required: 'with sign-in',
        minLength: 16,
        description: 'Registered client secret.'
      },
      {
        name: 'TIDE_USER_GROUPS',
        default: '',
        description:
          'Comma-separated groups allowed to sign in, matched exactly. Empty allows every user of the issuer. Needs sign-in: set without an issuer, tide refuses to start.'
      },
      {
        name: 'TIDE_ADMIN_GROUPS',
        default: '',
        description:
          'Comma-separated groups whose members manage every room. Always allowed to sign in. Needs sign-in, as above.'
      }
    ]
  },
  {
    title: 'Media',
    vars: [
      {
        name: 'TIDE_MEDIA_NODE_IP',
        default: '',
        defaultText: '127.0.0.1 on localhost, else discovered',
        description:
          "The IPv4 address browsers send audio and video to. Set it when tide is behind a load balancer or NAT, or has no internet access to discover it. It replaces the machine's own address, so with recording it must be local or routed back."
      },
      {
        name: 'TIDE_MEDIA_UDP_PORT',
        default: '7882',
        description: 'Media over UDP, on every interface. Open it publicly.'
      },
      {
        name: 'TIDE_MEDIA_TCP_PORT',
        default: '7881',
        description: 'Media for networks that block UDP. Open it publicly.'
      },
      {
        name: 'TIDE_MEDIA_API_PORT',
        default: '7880',
        description:
          "The media server's own API, on 127.0.0.1 only. Two tides on one host differ in it, in every other port and in TIDE_ADDR."
      },
      {
        name: 'TIDE_MEDIA_API_KEY',
        default: '',
        defaultText: 'generated per start',
        required: 'with recording',
        description:
          'Key ID the media server signs with, shared with the recorder. Letters, digits, `-` and `_`.'
      },
      {
        name: 'TIDE_MEDIA_API_SECRET',
        default: '',
        defaultText: 'generated per start',
        required: 'with recording',
        minLength: 32,
        description: 'The secret for that key.'
      },
      {
        name: 'TIDE_MEDIA_URL',
        default: '',
        defaultText: 'none: built in',
        description:
          'An external media server, as tide reaches it. Only to keep an installation from before tide carried its own; then the key, the secret and `TIDE_MEDIA_PUBLIC_URL` are required.'
      },
      {
        name: 'TIDE_MEDIA_PUBLIC_URL',
        default: '',
        defaultText: 'the base URL, as `ws` or `wss`',
        description:
          'The signaling address browsers get. Leave it unset unless `TIDE_MEDIA_URL` is set.'
      }
    ]
  },
  {
    title: 'Recording',
    vars: [
      {
        name: 'TIDE_S3_ENDPOINT',
        default: '',
        defaultText: 'none: no recording',
        description:
          'Turns recording on; needs sign-in and the recorder. The store as tide reaches it.'
      },
      {
        name: 'TIDE_S3_PUBLIC_ENDPOINT',
        default: '',
        defaultText: '`TIDE_S3_ENDPOINT`',
        description: 'The store as browsers reach it. Public HTTPS; download links name this host.'
      },
      {
        name: 'TIDE_S3_RECORDER_ENDPOINT',
        default: '',
        defaultText: '`TIDE_S3_ENDPOINT`',
        description: 'The store as the recorder reaches it.'
      },
      {
        name: 'TIDE_S3_BUCKET',
        default: 'tide-recordings',
        description: 'An existing bucket. Recordings go under `recordings/`.'
      },
      { name: 'TIDE_S3_REGION', default: 'us-east-1', description: 'Signing region.' },
      {
        name: 'TIDE_S3_ACCESS_KEY',
        default: '',
        required: 'with recording',
        description: 'Access key.'
      },
      {
        name: 'TIDE_S3_SECRET_KEY',
        default: '',
        required: 'with recording',
        minLength: 16,
        description: 'Secret key.'
      },
      {
        name: 'TIDE_RECORDER_REDIS_PASSWORD',
        default: '',
        required: 'with recording',
        minLength: 32,
        description:
          'Password the recorder uses for tide’s coordination endpoint. Hex keeps it safe in the recorder’s YAML.'
      },
      {
        name: 'TIDE_RECORDER_REDIS_ADDR',
        default: '127.0.0.1:6379',
        description:
          'Where tide serves that endpoint. Run the recorder in tide’s network and leave it alone; recording jobs carry the bucket’s credentials.'
      },
      {
        name: 'TIDE_RECORDER_TEMPLATE_URL',
        default: '',
        defaultText: '`TIDE_BASE_URL` + `/egress-template`',
        description:
          'The page the recorder loads to draw a meeting. With the recorder in tide’s network: `http://127.0.0.1:8080/egress-template`.'
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
          'Comma-separated IPs or CIDRs whose `X-Forwarded-For` tide believes. Only your proxy or ingress. An invalid entry stops startup.'
      },
      {
        name: 'TIDE_JOIN_RATE_LIMIT',
        default: '10',
        description:
          'Joins per client per minute. Also bounds room lookups and, when anonymous, room creation.'
      },
      {
        name: 'TIDE_WAIT_RATE_LIMIT',
        default: '20',
        description: 'Lobby wait streams per client per minute.'
      },
      {
        name: 'TIDE_LOGIN_RATE_LIMIT',
        default: '10',
        description: 'Sign-ins per client per minute, anonymous ones included.'
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
        description:
          'Turns on [transcripts](/docs/transcripts) and machine pairing. Needs recording.'
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
        ? `**Required ${v.required}**${v.minLength ? `, ${v.minLength}+ characters` : ''}. `
        : '';
      const fallback =
        v.defaultText ?? (v.default ? `\`${v.default}\`` : v.required ? '—' : 'empty');
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
    'tide reads only environment variables, and every one is optional: with none set it is an anonymous meeting server on `http://localhost:8080`. `TIDE_OIDC_ISSUER` turns sign-in on, and `TIDE_S3_ENDPOINT` recording. Outside development, tide refuses to start while a variable the mode needs is missing, too short, or a value from the repository’s development setup, and it logs its mode in one line at startup.',
    '',
    ...sections.flatMap((section) => [section, ''])
  ].join('\n');
}
