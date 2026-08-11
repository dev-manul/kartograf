<?php

namespace App\Service;

use App\Repo\UserRepository;
use App\Events\UserCreated as CreatedEvent;
use function App\Helpers\slug;

class UserService extends BaseService
{
    private UserRepository $repo;

    public function __construct(private readonly Mailer $mailer)
    {
    }

    public function run(Request $req, \App\Models\User $user): void
    {
        $r = new UserRepository();
        $e = new CreatedEvent($user);
        UserRepository::create();
        $max = UserRepository::MAX_ROWS;
        $name = CreatedEvent::class;
        Registry::$instances;

        $this->helper();
        self::helper();
        parent::boot();

        $this->repo->find(1);
        $this->mailer->send($e);
        $req->getBody();

        slug('x');
        strtolower('Y');
        \App\Helpers\other();

        if ($user instanceof CreatedEvent) {
            return;
        }
        try {
            $this->helper();
        } catch (NotFound | \RuntimeException $err) {
        }
    }
}
