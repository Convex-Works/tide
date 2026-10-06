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
        description: 'The address people open, with `https://`. No trailing slash.'
      },
      {
        name: 'TIDE_SESSION_SECRET',
        default: '',
        defaultText: 'random at each start, without sign-in',
        required: 'with sign-in',
        minLength: 32,
        description: 'Signs session cookies. Use random data.'
      },
      {
        name: 'TIDE_DB_PATH',
        default: './data/tide.db',
        description:
          'The SQLite database, with sign-in. The image uses `/data/tide.db`. Put a durable volume at `/data`.'
      },
      {
        name: 'TIDE_DEV_MODE',
        default: 'false',
        description: 'For local development only. Do not use it in production.'
      }
    ]
  },
  {
    title: 'Sign-in',
    vars: [
      {
        name: 'TIDE_OIDC_ISSUER',
        default: '',
        defaultText: 'none: no sign-in',
        description:
          'Turns sign-in on. Use the issuer URL exactly as the provider shows it. Without it, anyone can make a room.'
      },
      { name: 'TIDE_OIDC_CLIENT_ID', default: 'tide', description: 'The client ID.' },
      {
        name: 'TIDE_OIDC_CLIENT_SECRET',
        default: '',
        required: 'with sign-in',
        minLength: 16,
        description: 'The client secret.'
      },
      {
        name: 'TIDE_USER_GROUPS',
        default: '',
        description:
          'Groups that can sign in, with commas between them. Empty lets all users sign in.'
      },
      {
        name: 'TIDE_ADMIN_GROUPS',
        default: '',
        description: 'Groups that can manage all rooms, with commas between them.'
      }
    ]
  },
  {
    title: 'Media',
    vars: [
      {
        name: 'TIDE_MEDIA_NODE_IP',
        default: '',
        defaultText: '127.0.0.1 on localhost, else found',
        description:
          'The IPv4 address that browsers send media to. Set it only behind a load balancer or NAT. With recording, the recorder must also reach it.'
      },
      {
        name: 'TIDE_MEDIA_UDP_PORT',
        default: '7882',
        description: 'Media over UDP. Open it to the internet.'
      },
      {
        name: 'TIDE_MEDIA_TCP_PORT',
        default: '7881',
        description: 'Media when UDP is blocked. Open it to the internet.'
      },
      {
        name: 'TIDE_MEDIA_API_PORT',
        default: '7880',
        description:
          "The media server's API, on 127.0.0.1 only. Change it to run two tides on one host."
      },
      {
        name: 'TIDE_MEDIA_API_KEY',
        default: '',
        defaultText: 'random at each start',
        required: 'with recording',
        description:
          'The media key. tide and the recorder use the same one. Letters, digits, `-` and `_`.'
      },
      {
        name: 'TIDE_MEDIA_API_SECRET',
        default: '',
        defaultText: 'random at each start',
        required: 'with recording',
        minLength: 32,
        description: 'The secret for the media key.'
      },
      {
        name: 'TIDE_MEDIA_URL',
        default: '',
        defaultText: 'none: built in',
        description:
          'An external media server. Only for old installations. It needs the key, the secret and `TIDE_MEDIA_PUBLIC_URL`.'
      },
      {
        name: 'TIDE_MEDIA_PUBLIC_URL',
        default: '',
        defaultText: 'the base URL, as `ws` or `wss`',
        description: 'The external media server, as browsers reach it. Only with `TIDE_MEDIA_URL`.'
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
        description: 'Turns recording on. Needs sign-in. The bucket address, as tide reaches it.'
      },
      {
        name: 'TIDE_S3_PUBLIC_ENDPOINT',
        default: '',
        defaultText: '`TIDE_S3_ENDPOINT`',
        description: 'The bucket address, as browsers reach it. Use HTTPS.'
      },
      {
        name: 'TIDE_S3_RECORDER_ENDPOINT',
        default: '',
        defaultText: '`TIDE_S3_ENDPOINT`',
        description: 'The bucket address, as the recorder reaches it.'
      },
      {
        name: 'TIDE_S3_BUCKET',
        default: 'tide-recordings',
        description: 'The bucket. tide puts recordings in `recordings/`.'
      },
      { name: 'TIDE_S3_REGION', default: 'us-east-1', description: 'The bucket region.' },
      {
        name: 'TIDE_S3_ACCESS_KEY',
        default: '',
        required: 'with recording',
        description: 'The access key.'
      },
      {
        name: 'TIDE_S3_SECRET_KEY',
        default: '',
        required: 'with recording',
        minLength: 16,
        description: 'The secret key.'
      },
      {
        name: 'TIDE_RECORDER_REDIS_PASSWORD',
        default: '',
        required: 'with recording',
        minLength: 32,
        description: 'The Redis password. tide and the recorder use the same one. Use hex.'
      },
      {
        name: 'TIDE_RECORDER_REDIS_ADDR',
        default: '127.0.0.1:6379',
        description:
          'The Redis that tide and the recorder use, as host:port. Keep it private: jobs contain the bucket keys. Use one Redis for each tide.'
      },
      {
        name: 'TIDE_RECORDER_TEMPLATE_URL',
        default: '',
        defaultText: '`TIDE_BASE_URL` + `/egress-template`',
        description:
          'The page that the recorder opens to draw a meeting. Beside tide, use `http://127.0.0.1:8080/egress-template`.'
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
          'The addresses of your proxy, as IPs or CIDRs with commas between them. tide then trusts their `X-Forwarded-For`.'
      },
      {
        name: 'TIDE_JOIN_RATE_LIMIT',
        default: '10',
        description: 'Joins for each client in a minute. Also limits new rooms without sign-in.'
      },
      {
        name: 'TIDE_WAIT_RATE_LIMIT',
        default: '20',
        description: 'Lobby waits for each client in a minute.'
      },
      {
        name: 'TIDE_LOGIN_RATE_LIMIT',
        default: '10',
        description: 'Sign-ins for each client in a minute.'
      },
      {
        name: 'TIDE_PAIR_RATE_LIMIT',
        default: '10',
        description: 'Machine pairings for each client in a minute.'
      }
    ]
  },
  {
    title: 'Transcripts',
    vars: [
      {
        name: 'TIDE_TRANSCRIPTS',
        default: 'false',
        description: 'Turns [transcripts](/docs#transcripts) on. Needs recording.'
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
    'tide reads only these environment variables. All are optional. With none, tide runs without sign-in on `http://localhost:8080`. tide does not start if a required variable is missing or too short.',
    '',
    ...sections.flatMap((section) => [section, ''])
  ].join('\n');
}
