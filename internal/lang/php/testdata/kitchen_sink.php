<?php

declare(strict_types=1);

namespace App\Service;

use App\Contracts\{RepositoryInterface, CacheInterface as Cache};
use App\Models\User;
use Psr\Log\LoggerInterface as Logger;
use function App\Helpers\normalize;
use const App\Helpers\MAX_RETRIES;

/**
 * Handles user lifecycle operations.
 *
 * @author nobody
 */
#[AsService(tags: ['user'])]
final class UserService extends AbstractService implements RepositoryInterface, \Countable
{
    use \App\Traits\LoggerAwareTrait;
    use Cacheable;

    /** Default page size. */
    public const int PAGE_SIZE = 25;
    private const STATUSES = ['active', 'banned'];

    /** @var array<string, User> */
    private array $identityMap = [];
    protected static ?Logger $logger = null, $fallback = null;

    public function __construct(
        private readonly Cache $cache,
        protected UserRepository $repo,
    ) {
    }

    /**
     * Finds a user by id.
     */
    public function find(int $id): ?User
    {
        return $this->repo->find($id);
    }

    abstract protected function hydrate(array $row): User;

    public function count(): int
    {
        return \count($this->identityMap);
    }
}

interface Flushable
{
    public function flush(): void;
}

trait Cacheable
{
    public function cacheKey(): string
    {
        return static::class;
    }
}

enum Status: string
{
    /** Active user. */
    case Active = 'active';
    case Banned = 'banned';

    public function label(): string
    {
        return ucfirst($this->value);
    }
}

/**
 * Normalizes an email address.
 */
function normalizeEmail(string $email): string
{
    return strtolower(trim($email));
}

const GLOBAL_TTL = 3600;

namespace App\Legacy;

class OldService
{
    public function run(): void
    {
    }
}
